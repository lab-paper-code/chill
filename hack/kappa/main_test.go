package main

import (
	"fmt"
	"math"
	"testing"
	"time"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestDecodeSnapshotUsesCurrentWorkloadUID(t *testing.T) {
	snapshot, err := decodeSnapshot([]byte(`{"instances":[{"id":"current","workloadUID":"w","workloadUIDs":["old"]},{"id":"unbound","workloadUIDs":["old"]}]}`))
	if err != nil || len(snapshot.Instances) != 2 || snapshot.Instances[0].WorkloadUID != "w" || snapshot.Instances[1].WorkloadUID != "" {
		t.Fatalf("current snapshot decode: %+v %v", snapshot, err)
	}
	if _, err := decodeSnapshot([]byte(`{"instances":[]} {}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

// Synthetic PLC-like Nodes, margin 1 ms and d 1 ms: the b1 latency is the p99
// sample + 2 ms, the fastest option per Node counts, and options without d or
// for another Workload do not. The widest gap with at least half passing and
// two failing lies between lp (5 ms) and r5 (10 ms), so L = 7.1 ms.
func TestProposeSLOSplitsFastFromSlowNodes(t *testing.T) {
	w := &serving.Definition{Name: "plc", UID: "w"}
	snap := serving.SchedulerSnapshot{}
	option := func(node string, d bool, uid string, samples ...float64) {
		ref := serving.NodeReference{Name: node, UID: node + "-uid"}
		// Each option has its own runtime image, so a d matches one option.
		cond := profilederivation.CurvePlanSpec{PowerMode: "fixed", RuntimeImage: fmt.Sprint("image-", len(snap.Options)), LogicalModel: "plc"}
		p := &serving.Profile{ID: node, Condition: cond, Batches: []serving.BatchProfile{{Batch: 1, DurationNanos: samples}, {Batch: 2, DurationNanos: []float64{1}}}}
		snap.Options = append(snap.Options, serving.SchedulerOption{Instance: serving.SchedulerInstance{Node: ref, Profile: p, MaxBatch: 2}, CompatibleUIDs: []string{uid}})
		if d {
			snap.DispatchOccupancy = append(snap.DispatchOccupancy, serving.DispatchOccupancy{Node: ref, PowerMode: "fixed", RuntimeImage: cond.RuntimeImage, Model: "plc", PerRequest: time.Millisecond})
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

	latency := nodeLatencies(snap, 1, w, 0.99)
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
