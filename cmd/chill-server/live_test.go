package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

const (
	liveTestEvent = "event"
	liveTestDelta = "delta"
)

func TestLiveOnlyPublishesRecordedDecisionAndActionIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newLivePublisher(ctx)
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &schedulerService{observationLog: log, live: p}
	at := time.Now().UTC()
	d := serving.SchedulerDecision{At: at, Status: "Ready"}
	if err := service.recordDecision(d, serving.SchedulerSnapshot{At: at}); err != nil {
		t.Fatal(err)
	}
	result := servingkube.OperationResult{
		Operation: servingkube.Operation{ID: "same", Kind: "prepare", Node: serving.NodeReference{UID: "node"}},
		StartedAt: at, Status: "running",
	}
	p.action(result)
	result.StartedAt = at.Add(time.Second)
	p.action(result)
	sub, err := p.hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	frame, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		Type     string         `json:"type"`
		Decision *live.Decision `json:"decision"`
		Actions  []live.Action  `json:"actions"`
	}
	if err := json.Unmarshal(frame, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.Type != "snapshot" || baseline.Decision == nil ||
		baseline.Decision.At != liveAt(at) || len(baseline.Actions) != 2 {
		t.Fatalf("lost recorded decision or reused action ID: %s", frame)
	}
	if err := log.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.recordDecision(
		serving.SchedulerDecision{At: at.Add(time.Minute)}, serving.SchedulerSnapshot{},
	); err == nil {
		t.Fatal("closed observation store accepted a decision")
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if frame, err := sub.Next(short); err == nil {
		t.Fatalf("unrecorded decision was emitted: %s", frame)
	}
}

func projectionPublisher(at time.Time) *livePublisher {
	h := live.New(at)
	return &livePublisher{
		hub: h, metrics: live.NewMetrics(h, at),
		registered: map[string]live.Workload{}, instances: map[string]bool{},
	}
}

type projectionFrame struct {
	Type string `json:"type"`
	Seq  uint64 `json:"seq"`
	live.Snapshot
	Removed *live.Removed      `json:"removed"`
	Kind    string             `json:"kind"`
	Cluster *live.ClusterEvent `json:"cluster,omitempty"`
}

func nextProjection(t *testing.T, sub *live.Subscription) projectionFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var result projectionFrame
	if err := json.Unmarshal(frame, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func noProjection(t *testing.T, sub *live.Subscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if frame, err := sub.Next(ctx); err == nil {
		t.Fatalf("unchanged projection published: %s", frame)
	}
}

// S2-1: lifecycle, effective distribution and native progress stay independent.
func TestLiveRuntimeProjectionIndependentFacts(t *testing.T) {
	at := time.Now().UTC()
	p := projectionPublisher(at)
	defer p.hub.Close()
	sub, err := p.hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	baseline := nextProjection(t, sub)
	if baseline.Reservations == nil {
		t.Fatal("empty baseline reservations must be []")
	}
	states := []serving.WorkloadState{
		{Applied: &serving.Definition{UID: "a", Name: "pool-a"}, Admission: true, Pending: 3, ObservedAt: at},
		{Applied: &serving.Definition{UID: "b", Name: "pool-b"}, Pending: 1, ObservedAt: at},
	}
	physical := []serving.InstanceState{
		{ID: "preparing", OwnerWorkloadUID: "a"},
		{ID: "ready-unallocated", OwnerWorkloadUID: "a", Ready: true},
		{ID: "routing", Ready: true, Routable: map[string]bool{"a": true}, Waiting: map[string]int{"a": 2}},
		{ID: "processing", Ready: true, Routable: map[string]bool{"a": true}, Running: map[string]int{"a": 4}},
		{ID: "residual", Ready: true, Routable: map[string]bool{"a": false}, Running: map[string]int{"a": 1}},
		{ID: "removing", Ready: true, Stopped: true, Running: map[string]int{"a": 1}},
		{ID: "shared", Ready: true, Routable: map[string]bool{"a": true, "b": true},
			Running: map[string]int{"a": 2, "b": 3}, Waiting: map[string]int{"a": 1, "b": 2},
			AssignedUnknown: map[string]int{"a": 99},
			Workloads:       []serving.WorkloadReference{{UID: "a"}, {UID: "b"}}},
	}
	routing := map[string]map[string]int32{
		"a": {"routing": 20, "processing": 30, "shared": 50, "residual": 0},
		"b": {"shared": 100},
	}
	p.runtimeProjection(states, physical, routing)
	frame := nextProjection(t, sub)
	if frame.Type != liveTestDelta || frame.Seq != baseline.Seq+1 ||
		len(frame.Workloads) != 2 || len(frame.Instances) != len(physical) ||
		frame.Workloads[0].Pending != 3 || frame.Workloads[1].Pending != 1 {
		t.Fatalf("runtime capture was split or pending was lost: %+v", frame)
	}
	byID := map[string]live.Instance{}
	for _, row := range frame.Instances {
		byID[row.ID] = row
	}
	for _, original := range physical {
		row := byID[original.ID]
		if row.Ready != original.Ready || row.Stopped != original.Stopped ||
			row.OwnerWorkloadUID != original.OwnerWorkloadUID ||
			!reflect.DeepEqual(row.Routable, original.Routable) ||
			!reflect.DeepEqual(row.Running, original.Running) ||
			!reflect.DeepEqual(row.Waiting, original.Waiting) {
			t.Fatalf("%s facts changed: %+v", original.ID, row)
		}
		body, _ := json.Marshal(row)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(body, &fields)
		if fields["workloadUID"] != nil || fields["state"] != nil {
			t.Fatalf("single-POOL/inferred state survived: %s", body)
		}
	}
	if frame.Routing["a"]["shared"] != 50 || frame.Routing["b"]["shared"] != 100 ||
		len(frame.Routing["a"]) != 3 || byID["shared"].Waiting["a"] != 1 {
		t.Fatalf("multi-POOL routing or waiting was reclassified: %+v", frame)
	}
	// Runtime refreshes this read timestamp even when no fact changed.
	states[0].ObservedAt = at.Add(time.Second)
	physical[0], physical[1] = physical[1], physical[0]
	p.runtimeProjection(states, physical, routing)
	noProjection(t, sub)
	p.runtimeProjection(states, nil, nil)
	frame = nextProjection(t, sub)
	if frame.Type != liveTestDelta || len(frame.Instances) != 0 || frame.Removed == nil ||
		len(frame.Removed.Instances) != len(physical) || len(frame.Routing) != 0 {
		t.Fatalf("physical absence did not remove all identities atomically: %+v", frame)
	}
	p.runtimeProjection(states, nil, nil)
	noProjection(t, sub)
}

// S2-1: operations do not imply reservation release; only a new snapshot does.
func TestLiveDecisionReservationsAndNodeAvailability(t *testing.T) {
	at := time.Now().UTC()
	p := projectionPublisher(at)
	defer p.hub.Close()
	sub, err := p.hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	_ = nextProjection(t, sub)
	node := serving.NodeReference{UID: "node", Name: "edge"}
	snapshot := serving.SchedulerSnapshot{
		Nodes: []serving.SchedulerNode{{Node: node, PowerMode: "low", Ready: false, Measurement: true,
			Reasons: []string{"ExcludedNode", "PowerModeUnavailable"}, ObservedAt: at}},
		Reservations: []serving.SchedulerReservation{{
			Owner: "operation", Nodes: []serving.NodeReference{node}, WorkloadUIDs: []string{"a", "b"},
			PodUIDs: []string{"pod"}, Instances: []string{"shared"}, Target: &serving.SchedulerCandidate{ID: "large"},
		}},
	}
	decision := serving.SchedulerDecision{At: at, Status: "TransitionInProgress"}
	p.decision(decision, snapshot)
	frame := nextProjection(t, sub)
	if frame.Type != liveTestDelta || frame.Nodes[0].Ready || !frame.Nodes[0].Measurement ||
		!reflect.DeepEqual(frame.Nodes[0].Reasons, snapshot.Nodes[0].Reasons) ||
		len(frame.Reservations) != 1 || frame.Reservations[0].Reason != "미확인" ||
		!reflect.DeepEqual(frame.Reservations[0].WorkloadUIDs, []string{"a", "b"}) ||
		frame.Decision == nil || frame.Workloads == nil {
		t.Fatalf("decision facts were lost or split: %+v", frame)
	}
	for _, item := range []struct {
		value any
		keys  []string
	}{
		{frame.Nodes[0], []string{"watts", "targetPowerMode"}},
		{frame.Decision, []string{"recordRef"}},
		{frame.Reservations[0], []string{"target", "routeOnly"}},
	} {
		body, err := json.Marshal(item.value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range item.keys {
			if _, exists := fields[key]; exists {
				t.Fatalf("obsolete or large field %s survived: %s", key, body)
			}
		}
	}
	if event := nextProjection(t, sub); event.Type != liveTestEvent || event.Kind != "decision" {
		t.Fatalf("decision event missing: %+v", event)
	}
	p.decision(decision, snapshot)
	noProjection(t, sub)
	result := servingkube.OperationResult{
		Operation: servingkube.Operation{ID: "operation", Kind: "prepare", Node: node, WorkloadUID: "a"},
		StartedAt: at, Status: "running",
	}
	p.action(result)
	_ = nextProjection(t, sub)
	_ = nextProjection(t, sub)
	p.action(result)
	noProjection(t, sub)
	result.Status, result.EndedAt = liveOperationComplete, at.Add(time.Second)
	p.action(result)
	_ = nextProjection(t, sub)
	_ = nextProjection(t, sub)
	if err := sub.Resume(0, ""); err != nil {
		t.Fatal(err)
	}
	if retained := nextProjection(t, sub); len(retained.Reservations) != 1 {
		t.Fatalf("completed operation guessed reservation release: %+v", retained)
	}
	snapshot.Reservations[0].Reason = "ResidualExecution"
	decision.At = at.Add(2 * time.Second)
	p.decision(decision, snapshot)
	if retained := nextProjection(t, sub); retained.Reservations[0].Reason != "ResidualExecution" {
		t.Fatalf("reservation retention reason lost: %+v", retained)
	}
	_ = nextProjection(t, sub)
	snapshot.Reservations = nil
	decision.At = at.Add(3 * time.Second)
	p.decision(decision, snapshot)
	if released := nextProjection(t, sub); released.Reservations == nil || len(released.Reservations) != 0 {
		t.Fatalf("snapshot release was not an explicit empty replacement: %+v", released)
	}
	_ = nextProjection(t, sub)
}

func TestLiveClusterIssueEventHasNoEmptyDelta(t *testing.T) {
	at := time.Now().UTC()
	p := projectionPublisher(at)
	defer p.hub.Close()
	sub, err := p.hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	baseline := nextProjection(t, sub)
	for index, row := range []serving.ClusterObservation{
		{Kind: "issue", At: at, Event: "raised", Source: "observer", Reason: "NodeUnavailable"},
		{Kind: "issue", At: at.Add(time.Second), Event: "resolved", Source: "observer", PreviousReason: "NodeUnavailable"},
	} {
		p.cluster(row)
		frame := nextProjection(t, sub)
		if frame.Type != liveTestEvent || frame.Seq != baseline.Seq+uint64(index)+1 ||
			frame.Kind != "cluster" || frame.Cluster == nil ||
			frame.Cluster.Kind != row.Kind || frame.Cluster.At != liveAt(row.At) ||
			frame.Cluster.Event != row.Event || frame.Cluster.Source != row.Source ||
			frame.Cluster.Reason != row.Reason || frame.Cluster.PreviousReason != row.PreviousReason {
			t.Fatalf("issue facts changed or empty delta was emitted: %+v", frame)
		}
	}
}
