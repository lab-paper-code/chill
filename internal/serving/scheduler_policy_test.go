package serving

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func schedulerPolicyEdge(joules float64) SchedulerTransitionProjection {
	return SchedulerTransitionProjection{ExpectedJoules: &joules, TransitionComplete: true, Status: schedulerStatusFeasible}
}

func TestSchedulerWorkHistoryDisjointCursorAndReversal(t *testing.T) {
	start := time.Unix(100, 0)
	initial := SchedulerWorkHistory{Through: start, Values: map[string]float64{"a": 0}}
	first, err := AdvanceSchedulerWorkHistory(initial, start, start.Add(time.Second), map[string]map[string]SchedulerTransitionProjection{
		"a": {"a": schedulerPolicyEdge(10), "b": schedulerPolicyEdge(14)},
	})
	if err != nil || !reflect.DeepEqual(first.Values, map[string]float64{"a": 0, "b": 4}) {
		t.Fatalf("first interval = %+v, %v", first, err)
	}
	second, err := AdvanceSchedulerWorkHistory(first, first.Through, start.Add(2*time.Second), map[string]map[string]SchedulerTransitionProjection{
		"a": {"a": schedulerPolicyEdge(9), "b": schedulerPolicyEdge(1)},
		"b": {"a": schedulerPolicyEdge(1), "b": schedulerPolicyEdge(8)},
	})
	if err != nil || !reflect.DeepEqual(second.Values, map[string]float64{"a": 4, "b": 0}) {
		t.Fatalf("reversal reset accumulated source work: %+v, %v", second, err)
	}
	if !reflect.DeepEqual(initial.Values, map[string]float64{"a": 0}) || !reflect.DeepEqual(first.Values, map[string]float64{"a": 0, "b": 4}) {
		t.Fatal("advance mutated an earlier history")
	}
	for name, interval := range map[string][2]time.Time{
		"overlap":   {start, start.Add(3 * time.Second)},
		"gap":       {start.Add(3 * time.Second), start.Add(4 * time.Second)},
		"empty":     {second.Through, second.Through},
		"backwards": {second.Through, start},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := AdvanceSchedulerWorkHistory(second, interval[0], interval[1], nil)
			if err == nil || !reflect.DeepEqual(got, second) {
				t.Fatalf("invalid interval advanced history: %+v, %v", got, err)
			}
		})
	}
}

func TestSchedulerWorkHistoryDirectedReachabilityAndNormalization(t *testing.T) {
	start := time.Unix(100, 0)
	previous := SchedulerWorkHistory{Through: start, Values: map[string]float64{"a": 0, "b": 7}}
	got, err := AdvanceSchedulerWorkHistory(previous, start, start.Add(time.Second), map[string]map[string]SchedulerTransitionProjection{
		"a":              {"b": schedulerPolicyEdge(12), "new": schedulerPolicyEdge(20)},
		"b":              {"b": schedulerPolicyEdge(2), "new": schedulerPolicyEdge(1)},
		"unknown-source": {"unreachable": schedulerPolicyEdge(0)},
	})
	// a has no incoming edge, even though it had finite previous work. New pays
	// b's historical 7 plus its directed edge 1; b pays 7+2, not a free reset.
	if err != nil || !reflect.DeepEqual(got.Values, map[string]float64{"b": 1, "new": 0}) {
		t.Fatalf("directed min-plus/normalization = %+v, %v", got, err)
	}
	if !got.Through.Equal(start.Add(time.Second)) {
		t.Fatal("cursor did not advance to consumed receipt cutoff")
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "Inf") || strings.Contains(string(encoded), "NaN") {
		t.Fatalf("history is not finite JSON: %s, %v", encoded, err)
	}
}

func TestSchedulerWorkHistoryUnavailableEdgesDoNotBecomeFree(t *testing.T) {
	start := time.Unix(100, 0)
	unfinished := schedulerPolicyEdge(0)
	unfinished.TransitionComplete = false
	unknown := schedulerPolicyEdge(0)
	unknown.Status = schedulerStatusPending
	for name, edge := range map[string]SchedulerTransitionProjection{
		"null":       {TransitionComplete: true, Status: schedulerStatusFeasible},
		"unfinished": unfinished,
		"unknown":    unknown,
		"nan":        schedulerPolicyEdge(math.NaN()),
		"infinite":   schedulerPolicyEdge(math.Inf(1)),
		"negative":   schedulerPolicyEdge(-1),
	} {
		t.Run(name, func(t *testing.T) {
			previous := SchedulerWorkHistory{Through: start, Values: map[string]float64{"a": 0}}
			kernels := map[string]map[string]SchedulerTransitionProjection{"a": {"new": edge}}
			got, err := AdvanceSchedulerWorkHistory(previous, start, start.Add(time.Second), kernels)
			if err == nil || !reflect.DeepEqual(got, previous) {
				t.Fatalf("unavailable edge advanced cursor/state: %+v, %v", got, err)
			}
			kernels["a"]["a"] = schedulerPolicyEdge(3)
			got, err = AdvanceSchedulerWorkHistory(previous, start, start.Add(time.Second), kernels)
			if err != nil || !reflect.DeepEqual(got.Values, map[string]float64{"a": 0}) {
				t.Fatalf("unavailable destination gained work: %+v, %v", got, err)
			}
		})
	}
	previous := SchedulerWorkHistory{Through: start, Values: map[string]float64{"a": 0}}
	if got, err := AdvanceSchedulerWorkHistory(previous, start, start.Add(time.Second), nil); err == nil || !reflect.DeepEqual(got, previous) {
		t.Fatalf("missing edges advanced history: %+v, %v", got, err)
	}
	previous.Values["a"] = math.NaN()
	if _, err := AdvanceSchedulerWorkHistory(previous, start, start.Add(time.Second), nil); err == nil {
		t.Fatal("accepted nonfinite historical work")
	}
	previous.Values["a"] = math.MaxFloat64
	if _, err := AdvanceSchedulerWorkHistory(previous, start, start.Add(time.Second), map[string]map[string]SchedulerTransitionProjection{"a": {"a": schedulerPolicyEdge(math.MaxFloat64)}}); err == nil {
		t.Fatal("overflow became a finite path")
	}
}

func schedulerPolicyCandidate(uid string, joules float64, status string) SchedulerCandidate {
	return SchedulerCandidate{Nodes: []SchedulerNode{{Node: NodeReference{UID: uid}}}, ExpectedJoules: &joules, Status: status}
}

func TestSchedulerWorkSelectionSLOFirstAndPendingHistory(t *testing.T) {
	snapshot := SchedulerSnapshot{}
	cheap := schedulerPolicyCandidate("cheap", 1, schedulerStatusOverload)
	cheap.WorstExceedance = 0 // Deadline drops can overload despite zero exceedance.
	feasible := schedulerPolicyCandidate("feasible", 20, schedulerStatusFeasible)
	history := SchedulerWorkHistory{Values: map[string]float64{
		schedulerMustLogicalKey(t, snapshot, cheap):    0,
		schedulerMustLogicalKey(t, snapshot, feasible): 100,
	}}
	if index, reason := schedulerSelectWorkForTest(history, snapshot, []SchedulerCandidate{cheap, feasible}); index != 1 || reason != "" {
		t.Fatalf("history bought an SLO violation: %d %s", index, reason)
	}
	delete(history.Values, schedulerMustLogicalKey(t, snapshot, feasible))
	if index, reason := schedulerSelectWorkForTest(history, snapshot, []SchedulerCandidate{cheap, feasible}); index != -1 || !strings.HasPrefix(reason, "Pending:") {
		t.Fatalf("missing feasible history became zero or overload fallback: %d %s", index, reason)
	}
}

func TestSchedulerWorkSelectionUsesWorkAndDeterministicTies(t *testing.T) {
	snapshot := SchedulerSnapshot{}
	a := schedulerPolicyCandidate("a", 8, schedulerStatusFeasible)
	b := schedulerPolicyCandidate("b", 3, schedulerStatusFeasible)
	keyA, keyB := schedulerMustLogicalKey(t, snapshot, a), schedulerMustLogicalKey(t, snapshot, b)
	history := SchedulerWorkHistory{Values: map[string]float64{keyA: 0, keyB: 6}}
	if index, _ := schedulerSelectWorkForTest(history, snapshot, []SchedulerCandidate{b, a}); index != 1 {
		t.Fatal("ranked only prospective energy instead of W+A")
	}
	history.Values[keyB] = 5 // Equal score, lower actual energy wins.
	if index, _ := schedulerSelectWorkForTest(history, snapshot, []SchedulerCandidate{a, b}); index != 1 {
		t.Fatal("equal-score tie ignored lower actual energy")
	}
	a.Keep = true
	if index, _ := schedulerSelectWorkForTest(history, snapshot, []SchedulerCandidate{b, a}); index != 1 {
		t.Fatal("equal-score tie did not prefer continuation")
	}
	a.Keep = false
	*a.ExpectedJoules = 3
	history.Values[keyB] = 0
	want := keyA
	if keyB < keyA {
		want = keyB
	}
	for _, candidates := range [][]SchedulerCandidate{{a, b}, {b, a}} {
		index, reason := schedulerSelectWorkForTest(history, snapshot, candidates)
		if index < 0 || reason != "" || schedulerMustLogicalKey(t, snapshot, candidates[index]) != want {
			t.Fatalf("equal-energy tie is not stable by logical key: %d %s", index, reason)
		}
	}
}

func TestSchedulerWorkSelectionPreservesOverloadLifetimeUnknownOrdering(t *testing.T) {
	a := schedulerPolicyCandidate("a", 20, schedulerStatusOverload)
	a.WorstExceedance = 0.1
	b := schedulerPolicyCandidate("b", 1, schedulerStatusOverload)
	b.WorstExceedance = 0.2
	lifetime := schedulerPolicyCandidate("lifetime", 0, schedulerStatusLifetime)
	pending := schedulerPolicyCandidate("pending", 0, schedulerStatusPending)
	pending.ExpectedJoules = nil
	candidates := []SchedulerCandidate{pending, lifetime, b, a}
	if index, reason := schedulerSelectWorkForTest(SchedulerWorkHistory{}, SchedulerSnapshot{}, candidates); index != 3 || reason != "" {
		t.Fatalf("overload/lifetime/unknown ordering changed: %d %s", index, reason)
	}
	b.WorstExceedance = a.WorstExceedance
	if index, _ := schedulerSelectWorkForTest(SchedulerWorkHistory{}, SchedulerSnapshot{}, []SchedulerCandidate{a, b}); index != 1 {
		t.Fatal("overload energy tie-break changed")
	}
	if index, reason := schedulerSelectWorkForTest(SchedulerWorkHistory{}, SchedulerSnapshot{}, []SchedulerCandidate{pending, lifetime}); index != -1 || reason == "" {
		t.Fatalf("unknown or lifetime-infeasible candidate selected: %d %s", index, reason)
	}
}

func TestSchedulerWorkHistorySurvivesPhysicalReplacement(t *testing.T) {
	snapshot, candidate := schedulerLogicalKeyFixture()
	joules := 5.0
	candidate.Status, candidate.ExpectedJoules = schedulerStatusFeasible, &joules
	key := schedulerMustLogicalKey(t, snapshot, candidate)
	start := time.Unix(100, 0)
	previous := SchedulerWorkHistory{Through: start, Values: map[string]float64{key: 6, "another-state": 0}}
	oldID := candidate.Instances[0].ID
	candidate.Instances[0].ID = "replacement-pod"
	for _, route := range candidate.Routing {
		route[candidate.Instances[0].ID] = route[oldID]
		delete(route, oldID)
	}
	replacementKey := schedulerMustLogicalKey(t, snapshot, candidate)
	next, err := AdvanceSchedulerWorkHistory(previous, start, start.Add(time.Second), map[string]map[string]SchedulerTransitionProjection{
		key:             {replacementKey: schedulerPolicyEdge(2)},
		"another-state": {"another-state": schedulerPolicyEdge(1)},
	})
	if err != nil || next.Values[replacementKey] != 7 {
		t.Fatalf("replacement reset historical relative work: %+v, %v", next, err)
	}
	if index, reason := schedulerSelectWorkForTest(next, snapshot, []SchedulerCandidate{candidate}); index != 0 || reason != "" {
		t.Fatalf("physical replacement lost eligible logical history: %d %s", index, reason)
	}
}

func schedulerSelectWorkForTest(history SchedulerWorkHistory, snapshot SchedulerSnapshot, candidates []SchedulerCandidate) (int, string) {
	keys := make([]string, len(candidates))
	for index, candidate := range candidates {
		key, err := SchedulerLogicalConfigKey(snapshot, candidate)
		if err == nil {
			keys[index] = key
		}
	}
	return SelectSchedulerWorkCandidate(history, candidates, keys)
}

func TestSchedulerAlgorithmSettingIsExplicit(t *testing.T) {
	settings, _ := schedulerFixture()
	for _, algorithm := range []string{"", "unknown-policy", SchedulerAlgorithmFixedHorizon, SchedulerAlgorithmProjectedWorkHistory} {
		settings.Algorithm = algorithm
		valid := algorithm == SchedulerAlgorithmFixedHorizon || algorithm == SchedulerAlgorithmProjectedWorkHistory
		if err := settings.Validate(); (err == nil) != valid {
			t.Fatalf("algorithm %q validation=%v", algorithm, err)
		}
	}
	if _, reason := SelectSchedulerWorkCandidate(SchedulerWorkHistory{}, []SchedulerCandidate{{}}, nil); reason == "" {
		t.Fatal("misaligned key input was silently accepted")
	}
}
