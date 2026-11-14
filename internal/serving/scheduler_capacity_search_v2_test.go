package serving

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	v2 "github.com/lab-paper-code/chill/internal/scheduler/v2"
)

func TestSchedulerSettingsPlanSearch(t *testing.T) {
	s, _ := schedulerFixture()
	s.PlanSearchStrategy, s.PlanSearchEvaluations, s.PlanExactFinalists, s.PlanPolishActive = "", 0, 0, 0
	margin := 0.0
	in := SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &margin}
	got, err := in.Settings()
	if err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []string{"v1", "v2", "", "bad"} {
		got.PlanSearchStrategy = strategy
		err := got.Validate()
		if (err == nil) != (strategy == protocol.V1 || strategy == protocol.V2) {
			t.Fatalf("strategy %q: %v", strategy, err)
		}
	}
	got.PlanSearchStrategy = "v2"
	for _, field := range []*int{&got.PlanSearchEvaluations, &got.PlanExactFinalists} {
		old := *field
		*field = 0
		if got.Validate() == nil {
			t.Fatal("zero search setting accepted")
		}
		*field = old
	}
	got.PlanPolishActive = -1
	if got.Validate() == nil {
		t.Fatal("negative active search setting accepted")
	}
	got.PlanPolishActive = 0
	if err := got.Validate(); err != nil {
		t.Fatalf("all-plan active search rejected: %v", err)
	}
	strategy, evaluations, finalists, active, post, priced := "v2", 123, 7, 2, true, true
	in.PlanSearchStrategy, in.PlanSearchEvaluations, in.PlanExactFinalists, in.PlanPolishActive, in.PlanPostFilterFinalists, in.PlanPricedStart = &strategy, &evaluations, &finalists, &active, &post, &priced
	got, err = in.Settings()
	if err != nil || got.PlanSearchStrategy != strategy || got.PlanSearchEvaluations != evaluations || got.PlanExactFinalists != finalists || got.PlanPolishActive != active || !got.PlanPostFilterFinalists || !got.PlanPricedStart {
		t.Fatalf("explicit: %+v %v", got, err)
	}
	priced = false
	got, err = in.Settings()
	if err != nil || got.PlanPricedStart {
		t.Fatalf("explicit priced opt-out: %+v %v", got, err)
	}
}

func TestSchedulerSettingsPlanSearchConsumer(t *testing.T) {
	for _, strategy := range []string{"", protocol.V1} {
		t.Run("input="+strategy, func(t *testing.T) {
			s, snapshot, _ := capacityE1Fixture()
			input := SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &s.CompletionMarginMS}
			want := protocol.V2
			if strategy != "" {
				input.PlanSearchStrategy = &strategy
				want = strategy
			}
			settings, err := input.Settings()
			if err != nil {
				t.Fatal(err)
			}
			decision, err := EvaluateScheduler(context.Background(), settings, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Capacity == nil || decision.Capacity.Search == nil || decision.Capacity.Search.Strategy != want {
				t.Fatalf("input %q: expected %s search, got %+v", strategy, want, decision)
			}
		})
	}
}

func TestSchedulerSamplePlanSearchDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../config/samples/serving-scheduler.json")
	if err != nil {
		t.Fatal(err)
	}
	var sample struct {
		Settings json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(raw, &sample); err != nil {
		t.Fatal(err)
	}
	var input SchedulerSettingsInput
	if err := json.Unmarshal(sample.Settings, &input); err != nil {
		t.Fatal(err)
	}
	if input.PlanSearchStrategy == nil || input.PlanSearchEvaluations == nil || input.PlanExactFinalists == nil || input.PlanPolishActive == nil || input.PlanPostFilterFinalists == nil || input.PlanPricedStart == nil {
		t.Fatal("sample must explicitly set all six plan-search settings")
	}
	got, err := input.Settings()
	if err != nil {
		t.Fatal(err)
	}
	var omitted map[string]json.RawMessage
	if err := json.Unmarshal(sample.Settings, &omitted); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"planSearchStrategy", "planSearchEvaluations", "planExactFinalists", "planPolishActive", "planPostFilterFinalists", "planPricedStart"} {
		if _, ok := omitted[key]; !ok {
			t.Fatalf("sample missing %s", key)
		}
		delete(omitted, key)
	}
	raw, err = json.Marshal(omitted)
	if err != nil {
		t.Fatal(err)
	}
	var defaults SchedulerSettingsInput
	if err := json.Unmarshal(raw, &defaults); err != nil {
		t.Fatal(err)
	}
	want, err := defaults.Settings()
	if err != nil {
		t.Fatal(err)
	}
	for key, values := range map[string][2]any{
		"planSearchStrategy":      {got.PlanSearchStrategy, want.PlanSearchStrategy},
		"planSearchEvaluations":   {got.PlanSearchEvaluations, want.PlanSearchEvaluations},
		"planExactFinalists":      {got.PlanExactFinalists, want.PlanExactFinalists},
		"planPolishActive":        {got.PlanPolishActive, want.PlanPolishActive},
		"planPostFilterFinalists": {got.PlanPostFilterFinalists, want.PlanPostFilterFinalists},
		"planPricedStart":         {got.PlanPricedStart, want.PlanPricedStart},
	} {
		if values[0] != values[1] {
			t.Errorf("%s sample=%v default=%v", key, values[0], values[1])
		}
	}
}

func TestCapacityFunnelExactAndFallback(t *testing.T) {
	p := capacityTwinSwap(100, 0)
	keep := make([]int, len(p.plans))
	exact := p.evaluate(slices.Clone(keep), p.routeOf(keep))
	problem := p.searchProblem(time.Time{})
	evaluator := newCapacityExact(p)
	stats := v2.FunnelStats{}
	got := v2.Funnel(problem, evaluator, []protocol.Candidate{{Choice: keep, Joules: exact.joules + 3}}, &stats, nil)
	if got == nil || got.key != exact.key || stats.Exact != 1 || stats.Gap != 3 || stats.RawKey != exact.key || stats.RawJoules != exact.joules {
		t.Fatalf("funnel: %+v %+v", got, stats)
	}
	stats = v2.FunnelStats{}
	best := v2.ExactK1(problem, evaluator, &stats)
	if best == nil || !p.admits(best) || !p.bounded(best.choice) || stats.Exact < 2 {
		t.Fatalf("fallback: %+v %+v", best, stats)
	}
	p.settings.PlanPostFilterFinalists = true
	problem = p.searchProblem(time.Time{})
	stats = v2.FunnelStats{}
	filtered := v2.Funnel(problem, evaluator, []protocol.Candidate{{Choice: best.choice, Joules: best.joules}}, &stats, nil)
	want := p.minimumGain(p.payback(*best))
	if filtered == nil || filtered.key != want.key || filtered.joules != want.joules || stats.RawKey != best.key || stats.RawJoules != best.joules {
		t.Fatalf("postfilter: %+v want %+v", filtered, want)
	}
}

func TestCapacityFunnelPostFilterReverts(t *testing.T) {
	p := capacityTwinSwap(100, 150)
	alternative := *p.plans[0][1]
	alternative.after = 9 // saves 60 J and has no operation uncertainty
	alternative.operationBounds = nil
	alternative.record.Key = "alternative"
	p.plans[0] = append(p.plans[0], &alternative)
	p.keepWatts = p.evaluate([]int{0}, 0).joules / p.periodSeconds
	a, b := p.evaluate([]int{1}, 0), p.evaluate([]int{2}, 0)
	if !p.minimumGain(a).withinUncertainty || p.minimumGain(b).withinUncertainty {
		t.Fatal("fixture must reject the uncertain nominal winner and retain the alternative")
	}
	candidates := []protocol.Candidate{{Choice: a.choice, Joules: a.joules}, {Choice: b.choice, Joules: b.joules}}
	for _, enabled := range []bool{false, true} {
		p.settings.PlanPostFilterFinalists = enabled
		stats := v2.FunnelStats{}
		got := v2.Funnel(p.searchProblem(time.Time{}), newCapacityExact(p), candidates, &stats, nil)
		want := a
		wantReverts := 0
		if enabled {
			want = b
			wantReverts = 1
		}
		if got == nil || got.key != want.key || stats.RawKey != want.key || stats.RawJoules != want.joules || stats.Reverts != wantReverts {
			t.Fatalf("postfilter=%v got=%+v want=%+v stats=%+v", enabled, got, want, stats)
		}
	}
}

// Three nodes and two non-keep options exercise permutation and worker counts
// without replaying the historical E5 ten-minute, eight-node benchmark.
func capacityV2MultiOptionFixture() *capacityPlanner {
	p := capacityThreeNodeSwap()
	alternative := *p.plans[0][1]
	alternative.items = [][]capacityItem{{{id: "faster", routingWeight: 40, throughput: 40, order: .5, model: schedulerTestModel(.5)}}}
	alternative.record.Key, alternative.after = "faster", 26
	p.plans[0] = append(p.plans[0], &alternative)
	p.settings.ComparisonPeriod, p.periodSeconds = time.Second, 1
	capacityRawPlannerForTest(p)
	return p
}

func TestCapacitySearchV2Determinism(t *testing.T) {
	var baseline capacityResult
	var counts [2]int
	for trial := range 4 {
		p := capacityV2MultiOptionFixture()
		p.settings.PlanSearchStrategy = "v2"
		p.settings.PlanSearchWorkers = 1 + 2*(trial%2)
		if trial > 0 {
			rng := rand.New(rand.NewPCG(uint64(trial), 17))
			for _, plans := range p.plans {
				rng.Shuffle(len(plans)-1, func(i, j int) { plans[i+1], plans[j+1] = plans[j+1], plans[i+1] })
			}
		}
		got, err := p.search(time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if !p.admits(&got) || !p.bounded(got.choice) || len(got.feedback) == 0 ||
			len(p.searched.Steps) < 2 || p.searched.ExactEvaluated < 2 {
			t.Fatalf("fixture did not exercise exact multi-option search: %+v search=%+v", got, p.searched)
		}
		assertCapacitySearchSteps(t, p.searched, len(p.plans))
		current := [2]int{p.searched.SurrogateEvaluated, p.searched.ExactEvaluated}
		if trial == 0 {
			baseline, counts = got, current
			continue
		}
		if got.key != baseline.key || got.joules != baseline.joules || got.feasible != baseline.feasible ||
			!reflect.DeepEqual(got.feedback, baseline.feedback) || !reflect.DeepEqual(got.weights, baseline.weights) || current != counts {
			t.Fatalf("trial=%d result=%+v want=%+v counts=%v want=%v", trial, got, baseline, current, counts)
		}
	}
}

func TestCapacitySearchV2Steps(t *testing.T) {
	two := capacityThreeNodeSwap()
	two.plans, two.compared, two.snapshot.Nodes = two.plans[:2], two.compared[:2], two.snapshot.Nodes[:2]
	two.ws[0].rate, two.plans[0][1].after = 19, 15
	capacityLoad(two.settings, &two.snapshot, two.ws[0].d, two.ws[0].rate)
	cover := capacityThreeNodeSwap()
	cover.ws[0].rate = 45
	capacityLoad(cover.settings, &cover.snapshot, cover.ws[0].d, cover.ws[0].rate)
	countRecovery := capacityThreeNodeSwap()
	countRecovery.settings.MaxInstances = 2
	priced := capacityTwinSwap(100, 0)
	priced.settings.PlanPricedStart = true
	for _, tc := range []struct {
		name, phase string
		p           *capacityPlanner
		k, budget   int
	}{
		{"one", protocol.Polish, capacityTwinSwap(100, 0), 1, 0},
		{"two", protocol.Polish, two, 2, 0},
		{"combine", protocol.Combine, capacityThreeNodeSwap(), 3, 0},
		{"cover", protocol.Cover, cover, 1, 0},
		{"count-recovery", protocol.Cover, countRecovery, 1, 0},
		{"priced", "", priced, 0, 0},
		{"origin-only-budget", "", capacityTwinSwap(100, 0), 0, 1},
		{"accepted-budget", protocol.Polish, capacityTwinSwap(100, 0), 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			p.settings.PlanSearchStrategy = protocol.V2
			if tc.budget > 0 {
				p.settings.PlanSearchEvaluations = tc.budget
			}
			capacitySearchForTest(t, p)
			found, pricedOrigin, lostFeasibility := false, false, false
			var previous SchedulerPlanSearchStep
			for _, step := range p.searched.Steps {
				if step.Start != capacitySearchStartKeep && step.Start != "empty" && step.Start != "priced" ||
					step.ComparisonWindow != time.Duration(p.periodSeconds*1e9) {
					t.Fatalf("invalid start/window: %+v", step)
				}
				pricedOrigin = pricedOrigin || step.Start == "priced" && step.K == 0
				if step.Start == capacitySearchStartKeep && step.K > 0 {
					found = found || step.Phase == tc.phase && step.K == tc.k
					lostFeasibility = lostFeasibility || previous.Feasible && !step.Feasible
				}
				if step.K > 0 && len(p.plans) == 1 && (step.Evaluated != 1 ||
					!slices.Equal(step.ChangedNodes, []NodeReference{p.snapshot.Nodes[0].Node})) {
					t.Fatalf("single-node accepting sweep: %+v", step)
				}
				previous = step
			}
			if tc.phase != "" && !found || p.settings.PlanPricedStart && !pricedOrigin ||
				tc.name == "count-recovery" && !lostFeasibility {
				t.Fatalf("missing expected move/origin: %+v", p.searched.Steps)
			}
			if p.searched.BudgetCut || tc.budget > 0 && p.searched.TerminationReason != protocol.EvaluationBudget || p.searched.TrailingEvaluated == 0 {
				t.Fatalf("budget/trailing accounting: %+v", p.searched)
			}
		})
	}
}

func TestCapacitySearchV2Deadline(t *testing.T) {
	p := capacityTwinSwap(100, 0)
	p.settings.PlanSearchStrategy = protocol.V2
	if _, err := p.search(time.Now()); err != nil {
		t.Fatal(err)
	}
	if !p.searched.BudgetCut || p.searched.TerminationReason != protocol.Deadline {
		t.Fatalf("deadline accounting: %+v", p.searched)
	}
}

func TestSchedulerV2ZeroWitnessUsesPriorityThroughFinalAdmission(t *testing.T) {
	settings, snapshot, recoveryNode := capacityZeroWitnessFixture()
	first, second := snapshot.Workloads[0], snapshot.Workloads[1]
	// Offline Pending rows are deliberately unrelated: EvaluateScheduler has no row input.
	offline := []CapacityRow{{Status: CapacityPending}}
	chosen := map[string]string{}
	for _, uid := range []string{first.UID, second.UID} {
		settings.WorkloadPriority = []string{uid}
		decision, err := EvaluateScheduler(context.Background(), settings, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		capacityCheckZeroWitnessDecision(t, decision, uid, recoveryNode)
		capacityCheckZeroWitnessAdmission(t, settings, snapshot, decision, uid, recoveryNode)
		chosen[uid] = decision.Target.ID
		offline[0].Status = CapacityRejected
		again, err := EvaluateScheduler(context.Background(), settings, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if again.Target == nil || again.Target.ID != decision.Target.ID {
			t.Fatalf("offline row affected scheduling: %+v rows=%+v", again, offline)
		}
	}
	if chosen[first.UID] == chosen[second.UID] {
		t.Fatal("Workload priority failed to switch infeasible choice")
	}
}

func capacityZeroWitnessFixture() (SchedulerSettings, SchedulerSnapshot, NodeReference) {
	settings, snapshot := schedulerFixture()
	settings.PlanSearchStrategy = protocol.V2
	settings.SLOPriorityStrategy = SLOPriorityWorkload
	settings.ObservationWindow, settings.ControlInterval, settings.ComparisonPeriod = time.Second, time.Second, time.Second
	settings.MaxReplicas, settings.MaxInstances = 1, 3
	first := snapshot.Workloads[0]
	first.SLO.TargetLatency = time.Millisecond
	first.Admission.Policy = AdmissionPolicyReject
	second := poolsAddWorkload(&snapshot, "w2")
	settings.AutonomousWorkloads = []string{first.UID, second.UID}
	secondNode := poolsAddNode(&snapshot, "second", map[string]float64{"fixed": 10})
	secondInstance := poolsInstance(snapshot.Instances[0], "second", secondNode, "fixed", second.UID)
	snapshot.Instances = append(snapshot.Instances, secondInstance)
	snapshot.Options = append(snapshot.Options, poolsOption(secondInstance))
	recoveryNode := poolsAddNode(&snapshot, "recovery", map[string]float64{"fixed": 10})
	recovery := poolsInstance(snapshot.Instances[0], "recovery", recoveryNode, "fixed", first.UID)
	recovery.Profile.Power = schedulerTestPower(30)
	snapshot.Options = append(snapshot.Options, SchedulerOption{Instance: recovery, CompatibleUIDs: []string{first.UID, second.UID}})
	snapshot.Routing = map[string]map[string]int32{first.UID: {snapshot.Instances[0].ID: schedulerRouteUnits}, second.UID: {secondInstance.ID: schedulerRouteUnits}}
	snapshot.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, recoveryNode, "unloaded", schedulerState(recovery), "fixed"),
		poolsCost(schedulerActionRemove, snapshot.Instances[0].Node, schedulerState(snapshot.Instances[0]), "unloaded", "fixed"),
		poolsCost(schedulerActionRemove, secondNode, schedulerState(secondInstance), "unloaded", "fixed"),
	}
	capacityServiceRatesForTest(&snapshot, map[string]float64{snapshot.Instances[0].Profile.ID: 50, secondInstance.Profile.ID: 50, recovery.Profile.ID: 200})
	capacityLoad(settings, &snapshot, first, 10)
	capacityLoad(settings, &snapshot, second, 10)
	// No request is accepted by the old actor before the 1 ms prepare finishes.
	// With p100, one old-actor drop would preserve the old worst potential.
	for k := range snapshot.Arrivals {
		snapshot.Arrivals[k].At = snapshot.Arrivals[k].At.Add(50 * time.Millisecond)
	}
	capacityFeedbackForTest(settings, &snapshot)
	return settings, snapshot, recoveryNode
}

func capacityCheckZeroWitnessDecision(t *testing.T, decision SchedulerDecision, uid string, recoveryNode NodeReference) {
	t.Helper()
	if decision.Status != SchedulerStatusOverload || decision.Target == nil || decision.Target.Keep || decision.Capacity == nil || decision.Capacity.Search.Strategy != protocol.V2 {
		t.Fatalf("priority %s lost improved infeasible target: %+v", uid, decision)
	}
	if !slices.ContainsFunc(decision.Target.Instances, func(instance SchedulerInstance) bool {
		return instance.Node == recoveryNode && instance.WorkloadUID == uid
	}) {
		t.Fatalf("priority %s did not receive recovery: %+v", uid, decision.Target)
	}
	for _, workload := range decision.Capacity.Workloads {
		if len(workload.KeepTests) == 0 || len(workload.Tests) == 0 {
			t.Fatalf("missing current nominal tests: %+v", workload)
		}
		for _, evidence := range append(slices.Clone(workload.KeepTests), workload.Tests...) {
			if evidence.KappaHat != 0 || len(evidence.Tests) == 0 {
				t.Fatalf("fixture requires zero passing witness with completed tests: %+v", evidence)
			}
			if !slices.ContainsFunc(decision.Keep.Instances, func(instance SchedulerInstance) bool { return instance.ID == evidence.InstanceID }) &&
				!slices.ContainsFunc(decision.Target.Instances, func(instance SchedulerInstance) bool { return instance.ID == evidence.InstanceID }) {
				t.Fatalf("rate query escaped keep/selected instances: %+v", evidence)
			}
		}
	}
	selected := slices.IndexFunc(decision.Target.Feedback, func(score FeedbackWorkloadScore) bool { return score.UID == uid })
	baseline := slices.IndexFunc(decision.Keep.Feedback, func(score FeedbackWorkloadScore) bool { return score.UID == uid })
	if selected < 0 || baseline < 0 {
		t.Fatal("common feedback missing")
	}
	targetScore, keepScore := decision.Target.Feedback[selected], decision.Keep.Feedback[baseline]
	if targetScore.Satisfied || targetScore.FailureFraction != 1 || targetScore.SuccessQuantileNanos != nil || targetScore.PotentialExcess == nil || keepScore.PotentialExcess == nil || *targetScore.PotentialExcess >= *keepScore.PotentialExcess {
		t.Fatalf("infeasible recovery did not improve conditional potential: target=%+v keep=%+v", targetScore, keepScore)
	}
}

func capacityCheckZeroWitnessAdmission(t *testing.T, settings SchedulerSettings, snapshot SchedulerSnapshot, decision SchedulerDecision, uid string, recoveryNode NodeReference) {
	t.Helper()
	reason, err := SchedulerTargetAdmission(context.Background(), settings, snapshot, *decision.Target)
	if err != nil || reason != "" {
		t.Fatalf("improved infeasible target failed final admission: reason=%s err=%v", reason, err)
	}
	// NEW admission cannot take authority from an existing reservation,
	// even when that owner holds this exact frozen target.
	reserved := snapshot
	reservedTarget := *decision.Target
	reserveTargetForTest(&reserved, &reservedTarget)
	if reserved.Reservations[0].Owner != reservedTarget.ID || reserved.Reservations[0].Target != &reservedTarget {
		t.Fatal("fixture lost exact reservation ownership")
	}
	frozen := struct {
		Snapshot SchedulerSnapshot
		Target   SchedulerCandidate
	}{reserved, reservedTarget}
	before, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	reason, err = SchedulerTargetAdmission(context.Background(), settings, reserved, reservedTarget)
	if err != nil || reason == "" {
		t.Fatalf("NEW admission accepted reserved owner target: reason=%s err=%v", reason, err)
	}
	after, err := json.Marshal(struct {
		Snapshot SchedulerSnapshot
		Target   SchedulerCandidate
	}{reserved, reservedTarget})
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("NEW admission changed reserved snapshot or frozen target")
	}
	for _, changed := range []string{"allocation", "owner", "unavailable node"} {
		current := snapshot
		target := *decision.Target
		target.Instances = slices.Clone(target.Instances)
		current.Instances, current.Nodes, current.Workloads = slices.Clone(snapshot.Instances), slices.Clone(snapshot.Nodes), slices.Clone(snapshot.Workloads)
		switch changed {
		case "allocation":
			for k := range target.Instances {
				if target.Instances[k].Node == recoveryNode {
					target.Instances[k].Allocation.MemoryBytes++
				}
			}
		case "owner":
			foreign := *snapshot.Workloads[0]
			foreign.UID = "external-owner"
			current.Workloads = append(current.Workloads, &foreign)
			for k := range current.Instances {
				if current.Instances[k].WorkloadUID == uid {
					current.Instances[k].OwnerWorkloadUID = foreign.UID
				}
			}
		case "unavailable node":
			for k := range current.Nodes {
				if current.Nodes[k].Node == recoveryNode {
					current.Nodes[k].Ready = false
				}
			}
		}
		reason, err := SchedulerTargetAdmission(context.Background(), settings, current, target)
		if err != nil || reason == "" {
			t.Fatalf("%s escaped final physical admission: reason=%s err=%v", changed, reason, err)
		}
	}
}

func TestSchedulerV2FeasibleFinalAdmissionUsesOriginalEnergy(t *testing.T) {
	settings, snapshot, retired, _ := capacityR5Fixture()
	settings.PlanSearchStrategy = protocol.V2
	settings.Controls.Routing = false
	snapshot.Instances[1].Profile.Power = schedulerTestPower(60)
	snapshot.Options[1].Instance.Profile = snapshot.Instances[1].Profile
	since := snapshot.At.Add(-settings.ObservationWindow)
	snapshot.Instances[1].UnroutedSince = &since
	decision, err := EvaluateScheduler(context.Background(), settings, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Target == nil || decision.Target.Keep || decision.Status != schedulerStatusFeasible || poolsOn(*decision.Target, retired) || decision.Target.ExpectedJoules == nil || decision.Keep.ExpectedJoules == nil || *decision.Target.ExpectedJoules >= *decision.Keep.ExpectedJoules {
		t.Fatalf("feasible lower-energy finalist lost final admission: %+v", decision)
	}
	reason, err := SchedulerTargetAdmission(context.Background(), settings, snapshot, *decision.Target)
	if err != nil || reason != "" {
		t.Fatalf("lower-energy feasible target failed final admission: reason=%s err=%v", reason, err)
	}
	for _, score := range decision.Target.Feedback {
		if !score.Satisfied || score.SuccessQuantileNanos == nil || *score.SuccessQuantileNanos > float64(snapshot.Workloads[0].SLO.TargetLatency) {
			t.Fatalf("feasible target lacks finite successful SLO evidence: %+v", score)
		}
	}
}

func TestCapacityV2DeltaDoesNotConsumeExactReplay(t *testing.T) {
	settings, snapshot, _ := capacityE1Fixture()
	settings.PlanSearchStrategy = protocol.V2
	planner := capacityPlannerForTest(t, settings, snapshot)
	cached := func() int {
		count := 0
		planner.feedbackCache.Range(func(_, _ any) bool { count++; return true })
		return count
	}
	if cached() != 0 {
		t.Fatal("plan construction consumed exact replay")
	}
	table := planner.planTable()
	problem := planner.scalarProblem(table, time.Time{})
	scalarKeep := make([]int, len(planner.plans))
	for n := range scalarKeep {
		scalarKeep[n] = table.byPlan[n][0]
	}
	state := problem.NewState(scalarKeep)
	for range 3 {
		for n, plans := range planner.plans {
			for j := range plans {
				state.Delta(n, table.byPlan[n][j])
			}
		}
	}
	if cached() != 0 {
		t.Fatal("scalar Delta invoked demand replay")
	}
	evaluator := newCapacityExact(planner)
	exactKeep := planner.evaluateCurrentKeep(planner.periodSeconds)
	if cached() != 1 || len(exactKeep.feedback) == 0 {
		t.Fatalf("keep did not consume common replay: %+v", exactKeep)
	}
	choice := capacityChoiceForTest(t, planner, map[string]string{"genesis3": "profile@genesis3-fixed/b1/w1"})
	stats := v2.FunnelStats{}
	finalist := v2.Funnel(planner.searchProblem(time.Time{}), evaluator, []protocol.Candidate{{Choice: choice}}, &stats, nil)
	if finalist == nil || stats.Exact != 1 || cached() != 2 || len(finalist.feedback) == 0 {
		t.Fatalf("finalist replay boundary missing: result=%+v stats=%+v cached=%d", finalist, stats, cached())
	}
	for n, plans := range planner.plans {
		for j := range plans {
			state.Delta(n, table.byPlan[n][j])
		}
	}
	if cached() != 2 {
		t.Fatal("post-finalist Delta consumed replay")
	}
}

func TestCapacityExactKeepPreservesActualRoutesAndEnergyOffset(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.ObservationWindow, settings.ControlInterval, settings.ComparisonPeriod = time.Second, time.Second, time.Second
	settings.MaxReplicas, settings.MaxInstances = 2, 2
	definition := snapshot.Workloads[0]
	definition.SLO.TargetLatency = 500 * time.Millisecond
	secondNode := poolsAddNode(&snapshot, "second", map[string]float64{"fixed": 10})
	second := poolsInstance(snapshot.Instances[0], "second", secondNode, "fixed", definition.UID)
	snapshot.Instances = append(snapshot.Instances, second)
	snapshot.Options = append(snapshot.Options, poolsOption(second))
	capacityServiceRatesForTest(&snapshot, map[string]float64{snapshot.Instances[0].Profile.ID: 100, second.Profile.ID: 10})
	snapshot.Routing = map[string]map[string]int32{definition.UID: {snapshot.Instances[0].ID: 1, second.ID: 999}}
	capacityLoad(settings, &snapshot, definition, 50)
	capacityFeedbackForTest(settings, &snapshot)
	planner := capacityPlannerForTest(t, settings, snapshot)
	actual := planner.evaluateCurrentKeep(planner.periodSeconds)
	// Gateway admission can make keep equally good; assess the reroute itself,
	// before evaluate's keep fallback, to retain this route/energy contract check.
	selected := planner.evaluateAtRoute(make([]int, len(planner.plans)), 0, nil)
	if actual.keep != true || selected.keep || reflect.DeepEqual(actual.weights, selected.weights) || selected.keepJoules != actual.joules {
		t.Fatalf("actual keep/reroute distinction lost: actual=%+v selected=%+v", actual, selected)
	}
	evaluator := newCapacityExact(planner)
	evaluator.SeedKeep(selected)
	if planner.keepWatts != actual.joules/planner.periodSeconds {
		t.Fatal("reroute replaced actual keep energy offset")
	}
	if err := planner.testDecisionRates(selected); err != nil {
		t.Fatal(err)
	}
	currentRate := planner.keepRates[0][0].Tests[0].Rate
	selectedRate := planner.selectedRates[0][0].Tests[0].Rate
	selectedWeight := selected.weights[0][snapshot.Instances[0].ID]
	if currentRate != .05 || selectedWeight == 0 || selectedRate != 50*float64(selectedWeight)/schedulerRouteUnits {
		t.Fatalf("nominal queries did not use actual integer routes: keep=%g selected=%g", currentRate, selectedRate)
	}
	decision := planner.decide(SchedulerDecision{Keep: planner.keep}, selected)
	if decision.Target == nil || decision.Target.Keep || !slices.ContainsFunc(decision.Target.Actions, func(action Action) bool { return action.Kind == schedulerActionRoute }) {
		t.Fatalf("chosen same-physical reroute was not an actual target: %+v", decision)
	}
}
