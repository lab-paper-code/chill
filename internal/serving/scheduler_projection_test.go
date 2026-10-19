package serving

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func schedulerProjectionFixture() (SchedulerSettings, SchedulerSnapshot, SchedulerCandidate, SchedulerCandidate) {
	settings, snapshot := schedulerFixture()
	first := snapshot.Instances[0]
	second := schedulerTestCPU(first)
	second.ID = "projection-destination"
	profile := *second.Profile
	profile.IdleBaselineWatts = 14
	second.Profile = &profile
	snapshot.Options = append(snapshot.Options, SchedulerOption{Instance: second, CompatibleUIDs: first.WorkloadUIDs})
	source := SchedulerCandidate{Nodes: snapshot.Nodes, Instances: []SchedulerInstance{first}}
	target := SchedulerCandidate{Nodes: snapshot.Nodes, Instances: []SchedulerInstance{second}}
	add := func(id, kind, from, to string, duration time.Duration, joules float64, before []string) {
		snapshot.Costs = append(snapshot.Costs, OperationCost{ID: id, Node: first.Node, Kind: kind, From: from, To: to, Trials: []OperationTrial{{Duration: duration, Joules: joules}}, EnergyScope: "whole-wall", Meter: first.IdleMeter, Scope: schedulerNodeWallScope, PowerMode: "fixed", BeforeState: before, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: id, Size: 1}})
	}
	a, b := schedulerState(first), schedulerState(second)
	add("prepare-second", schedulerActionPrepare, "unloaded", b, 20*time.Millisecond, 4, []string{a})
	add("remove-first", schedulerActionRemove, a, "unloaded", 10*time.Millisecond, 2, []string{a, b})
	add("prepare-first", schedulerActionPrepare, "unloaded", a, 10*time.Millisecond, 1, []string{b})
	add("remove-second", schedulerActionRemove, b, "unloaded", 20*time.Millisecond, 3, []string{a, b})
	return settings, snapshot, source, target
}

func schedulerProjectForTest(t testing.TB, settings SchedulerSettings, snapshot SchedulerSnapshot, source, target SchedulerCandidate) SchedulerTransitionProjection {
	t.Helper()
	projection, err := ProjectSchedulerTransition(context.Background(), settings, snapshot, source, target)
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func TestSchedulerProjectionDirectedWholePoolAndDiagonal(t *testing.T) {
	settings, snapshot, source, target := schedulerProjectionFixture()
	for _, test := range []struct {
		name         string
		from, to     SchedulerCandidate
		joules       float64
		used, unused []string
	}{
		{"forward", source, target, 6.98, []string{"prepare-second", "remove-first"}, []string{"prepare-first", "remove-second"}},
		{"reverse", target, source, 4.84, []string{"prepare-first", "remove-second"}, []string{"prepare-second", "remove-first"}},
		{"diagonal", source, source, 1.2, nil, []string{"prepare-second", "remove-first", "prepare-first", "remove-second"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := schedulerProjectForTest(t, settings, snapshot, test.from, test.to)
			if result.ExpectedJoules == nil || !result.TransitionComplete || result.Status != schedulerStatusFeasible || math.Abs(*result.ExpectedJoules-test.joules) > 1e-9 {
				t.Fatalf("projection=%+v; want %gJ", result, test.joules)
			}
			for _, id := range test.used {
				if !slices.Contains(result.References, id) {
					t.Fatalf("missing used primitive %s in %v", id, result.References)
				}
			}
			for _, id := range test.unused {
				if slices.Contains(result.References, id) {
					t.Fatalf("unused primitive %s in %v", id, result.References)
				}
			}
		})
	}
	// An uninvolved pool member still contributes on exactly the same interval.
	idleNode := snapshot.Nodes[0]
	idleNode.Node.UID += "-idle"
	idleNode.Node.Name += "-idle"
	idleBaseline := snapshot.Baselines[0]
	idleBaseline.Node, idleBaseline.ID, idleBaseline.Meter = idleNode.Node, "passive-baseline", schedulerTestSecondMeter
	snapshot.Nodes = append(append([]SchedulerNode(nil), snapshot.Nodes...), idleNode)
	snapshot.Baselines = append(snapshot.Baselines, idleBaseline)
	source.Nodes, target.Nodes = snapshot.Nodes, snapshot.Nodes
	wholePool := schedulerProjectForTest(t, settings, snapshot, source, target)
	if wholePool.ExpectedJoules == nil || math.Abs(*wholePool.ExpectedJoules-7.98) > 1e-9 || !slices.Contains(wholePool.References, idleBaseline.ID) {
		t.Fatalf("uninvolved pool baseline missing: %+v", wholePool)
	}
}

// Spec ②: an unchanged Node without a baseline does not block projected-work kernels.
func TestSchedulerWorkKernelsIgnoreUnchangedNodeWithoutBaseline(t *testing.T) {
	settings, snapshot, source, target := schedulerProjectionFixture()
	bare := snapshot.Nodes[0]
	bare.Node.UID += "-bare"
	bare.Node.Name += "-bare"
	snapshot.Nodes = append(append([]SchedulerNode(nil), snapshot.Nodes...), bare)
	source.Nodes, target.Nodes = snapshot.Nodes, snapshot.Nodes
	kernels, err := ProjectSchedulerWorkKernels(context.Background(), settings, snapshot, map[string]SchedulerCandidate{"a": source, "b": target}, map[string]float64{"a": 0})
	if err != nil {
		t.Fatal(err)
	}
	for destination, edge := range kernels["a"] {
		if edge.Status != schedulerStatusFeasible || edge.ExpectedJoules == nil {
			t.Fatalf("edge a->%s pending: %+v", destination, edge)
		}
	}
}

func TestSchedulerProjectionRejectsIncompleteAndMissingDirectedPrimitive(t *testing.T) {
	settings, snapshot, source, target := schedulerProjectionFixture()
	settings.Horizon, settings.ObservationWindow = 25*time.Millisecond, 25*time.Millisecond
	incomplete := schedulerProjectForTest(t, settings, snapshot, source, target)
	if incomplete.ExpectedJoules != nil || incomplete.TransitionComplete {
		t.Fatalf("partial edge was accepted: %+v", incomplete)
	}
	schedulerRequireReason(t, incomplete.Reasons, "ProjectionTransitionExceedsInterval")
	settings.Horizon, settings.ObservationWindow = 100*time.Millisecond, 100*time.Millisecond
	snapshot.Costs = snapshot.Costs[1:]
	missing := schedulerProjectForTest(t, settings, snapshot, source, target)
	if missing.ExpectedJoules != nil || missing.TransitionComplete {
		t.Fatalf("missing directed edge accepted: %+v", missing)
	}
	schedulerRequireReason(t, missing.Reasons, "MissingOperationCost")
	reverse := schedulerProjectForTest(t, settings, snapshot, target, source)
	if reverse.ExpectedJoules == nil || !reverse.TransitionComplete {
		t.Fatalf("unaffected reverse edge invalidated: %+v", reverse)
	}
}

func TestSchedulerProjectionDiscardsActualProgressWithoutMutation(t *testing.T) {
	settings, snapshot, source, target := schedulerProjectionFixture()
	snapshot.At = snapshot.At.Add(10 * time.Millisecond)
	snapshot.Arrivals = []SchedulerArrival{{ID: "one", At: snapshot.ObservationEnd.Add(-settings.ObservationWindow), Definition: snapshot.Workloads[0], Accepted: true}}
	clean := schedulerProjectForTest(t, settings, snapshot, source, target)
	snapshot.Actions = []Action{{ID: "actual-running", Kind: schedulerActionPrepare, Status: phaseRunning}}
	snapshot.InProgressTarget = &source
	snapshot.Outstanding = []SchedulerRequest{{ID: "actual-running", Phase: phaseRunning}}
	snapshot.RoutingCredits = map[string]map[string]int64{"w1": {"i1": 123}}
	snapshot.Instances[0].ArrivalAges = map[string][]time.Duration{"w1": {time.Millisecond}}
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	projected := schedulerProjectForTest(t, settings, snapshot, source, target)
	after, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("projection mutated observed physical state")
	}
	if !reflect.DeepEqual(clean, projected) || clean.ExpectedJoules == nil || math.Abs(*clean.ExpectedJoules-7.98) > 1e-9 {
		t.Fatalf("actual progress leaked or receipt not replayed once: clean=%+v projected=%+v", clean, projected)
	}
	// Logical equality cannot create movement merely because physical IDs differ.
	renamed := source
	renamed.Instances = append([]SchedulerInstance(nil), source.Instances...)
	renamed.Instances[0].ID, renamed.Instances[0].Ready = "different-physical-id", false
	renamed.Instances[0].ObservedAt = time.Time{}
	diagonal := schedulerProjectForTest(t, settings, snapshot, renamed, source)
	if diagonal.ExpectedJoules == nil || !diagonal.TransitionComplete || math.Abs(*diagonal.ExpectedJoules-2.2) > 1e-9 {
		t.Fatalf("hypothetical source required fabricated native readiness: %+v", diagonal)
	}
}
