package main

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/test/fixtures"
)

func TestDecodeSnapshotUsesCurrentWorkloadUID(t *testing.T) {
	snapshot, err := decodeSnapshot([]byte(`{"instances":[{"id":"current","workloadUID":"w"},{"id":"unbound"}]}`))
	if err != nil || len(snapshot.Instances) != 2 || snapshot.Instances[0].WorkloadUID != "w" || snapshot.Instances[1].WorkloadUID != "" {
		t.Fatalf("current snapshot decode: %+v %v", snapshot, err)
	}
	if _, err := decodeSnapshot([]byte(`{"instances":[]} {}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	for _, retired := range []string{
		`{"instances":[{"allocation":{"cpus":[0]}}]}`,
		`{"instances":[{"profile":{"condition":{"cpu":{"request":"1"}}}}]}`,
		`{"nodes":[{"modeCPUs":{"fixed":[0]}}]}`,
		`{"instances":[{"workloadUIDs":["old"]}]}`,
	} {
		if _, err := decodeSnapshot([]byte(retired)); err == nil {
			t.Fatalf("retired allocation/evidence crossed replay cutover: %s", retired)
		}
	}
}

func TestTickWithoutCapacityRows(t *testing.T) {
	at := time.Unix(100, 0)
	snapshot := serving.SchedulerSnapshot{At: at, ObservationEnd: at}
	if err := tick(serving.SchedulerSettings{}, snapshot, 0, 1); err != nil {
		t.Fatalf("offline tick without a rows file: %v", err)
	}
	if !snapshot.At.Equal(at) || !snapshot.ObservationEnd.Equal(at) {
		t.Fatal("tick changed captured timestamp boundaries")
	}
}

// Synthetic PLC-like Nodes, margin 1 ms and d 1 ms: the b1 latency is the p99
// sample + 2 ms, the fastest option per Node counts. The unmeasured fast option
// uses configured d=20ms rather than disappearing or assuming zero. The widest
// gap with at least half passing and two failing is lp/r5, so L = 7.1 ms.
func TestProposeSLOSplitsFastFromSlowNodes(t *testing.T) {
	w := &serving.Definition{Name: "plc", UID: "w"}
	snap := serving.SchedulerSnapshot{}
	option := func(node string, d bool, uid string, samples ...float64) {
		ref := serving.NodeReference{Name: node, UID: node + "-uid"}
		// Power mode scopes d; image differences are measurement history only.
		cond := profilederivation.CurvePlanSpec{PowerMode: fmt.Sprint("mode-", len(snap.Options)), RuntimeImage: "current-image", LogicalModel: "plc"}
		paired := []firstfloor.ServiceSample{fixtures.ServiceSample("physical-trial", 0, int64(samples[0]))}
		for i, service := range samples {
			paired = append(paired, fixtures.ServiceSample("physical-trial", uint64(i+1), int64(service)))
		}
		p := &serving.Profile{ID: node, Condition: cond, Batches: []serving.BatchProfile{{Batch: 1, ServiceSamples: paired}, {Batch: 2, ServiceSamples: []firstfloor.ServiceSample{fixtures.ServiceSample("physical-trial", 0, 1), fixtures.ServiceSample("physical-trial", 1, 1)}}}}
		snap.Options = append(snap.Options, serving.SchedulerOption{Instance: serving.SchedulerInstance{Node: ref, Profile: p, MaxBatch: 2}, CompatibleUIDs: []string{uid}})
		if d {
			snap.DispatchOccupancy = append(snap.DispatchOccupancy, serving.DispatchOccupancy{Node: ref, PowerMode: cond.PowerMode, RuntimeImage: "historical-image", Model: "plc", PerRequest: time.Millisecond})
		}
	}
	spread := make([]float64, 100)
	for i := range spread {
		spread[i] = float64(i+1) * 1e5
	}
	option("g2", true, "w", 1e6, 1e6, 1e6)
	option("g2", true, "w", 20e6)
	option("g2", true, "other", 1e5)
	option("g3", true, "w", 2e6)
	option("lp", true, "w", 3e6)
	option("r5", true, "w", 8e6)
	option("r5", false, "w", 1e5)
	option("op", true, "w", spread...)
	option("jn", true, "w", 14e6)

	settings := serving.SchedulerSettings{CompletionMarginMS: 1, DefaultDispatchOccupancyNanos: int64(20 * time.Millisecond)}
	latency := nodeLatencies(settings, snap, w, 0.99)
	want := map[string]float64{"g2": 3, "g3": 4, "lp": 5, "r5": 10, "op": 11.9, "jn": 16}
	if len(latency) != len(want) {
		t.Fatalf("latency %v, want %v", latency, want)
	}
	for n, v := range want {
		if math.Abs(latency[n]-v) > 1e-9 {
			t.Fatalf("latency %v, want %v", latency, want)
		}
	}
	if l, err := proposeSLO(latency); err != nil || math.Abs(l-7.1) > 1e-9 {
		t.Fatalf("proposed %g, %v; want 7.1", l, err)
	}
	if _, err := proposeSLO(map[string]float64{"a": 1, "b": 2, "c": 3}); err == nil {
		t.Fatal("3 Nodes cannot have half passing and two failing")
	}
	if _, err := proposeSLO(map[string]float64{"a": 1, "b": 5, "c": 5, "d": 5}); err == nil {
		t.Fatal("no gap among the candidate splits must fail")
	}
}
