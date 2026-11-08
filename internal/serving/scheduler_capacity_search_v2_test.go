package serving

import (
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
	if got.PlanSearchStrategy != "v1" || got.PlanSearchEvaluations != 500000 || got.PlanExactFinalists != 16 || got.PlanPolishActive != 8 || got.PlanPostFilterFinalists || !got.PlanPricedStart {
		t.Fatalf("defaults: %+v", got)
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
	p := e5Planner(e5Mixes[0], evaluationScaled(1, 0), e5Start, nil)
	keep := make([]int, len(p.plans))
	exact := p.evaluate(slices.Clone(keep), p.routeOf(keep))
	problem := p.searchProblem(time.Time{})
	evaluator := newCapacityExact(p)
	stats := v2.FunnelStats{}
	got := v2.Funnel(problem, evaluator, []protocol.Candidate{{Choice: keep, Joules: exact.joules + 3}}, &stats)
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
	filtered := v2.Funnel(problem, evaluator, []protocol.Candidate{{Choice: best.choice, Joules: best.joules}}, &stats)
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
	p.keepWatts = p.evaluate([]int{0}, 0).joules / p.period
	a, b := p.evaluate([]int{1}, 0), p.evaluate([]int{2}, 0)
	candidates := []protocol.Candidate{{Choice: a.choice, Joules: a.joules}, {Choice: b.choice, Joules: b.joules}}
	for _, enabled := range []bool{false, true} {
		p.settings.PlanPostFilterFinalists = enabled
		stats := v2.FunnelStats{}
		got := v2.Funnel(p.searchProblem(time.Time{}), newCapacityExact(p), candidates, &stats)
		want := a
		if enabled {
			want = b
		}
		if got == nil || got.key != want.key || stats.Reverts != 1 || stats.RawKey != want.key || stats.RawJoules != want.joules {
			t.Fatalf("postfilter=%v got=%+v want=%+v stats=%+v", enabled, got, want, stats)
		}
	}
}

func TestCapacitySearchV2Determinism(t *testing.T) {
	var baseline capacityResult
	var counts [2]int
	for trial := range 4 {
		p := e5Planner(map[int]float64{0: 60, 1: 40, 2: 1.2}, evaluationScaled(2, 2), e5Start, nil)
		p.settings.PlanSearchStrategy = "v2"
		p.settings.PlanSearchWorkers = 1 + 2*(trial%2)
		if trial > 0 {
			rng := rand.New(rand.NewPCG(uint64(trial), 17))
			for _, plans := range p.plans {
				rng.Shuffle(len(plans)-1, func(i, j int) { plans[i+1], plans[j+1] = plans[j+1], plans[i+1] })
			}
		}
		got, err := p.search(time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		assertCapacitySearchSteps(t, p.searched, len(p.plans))
		current := [2]int{p.searched.SurrogateEvaluated, p.searched.ExactEvaluated}
		if trial == 0 {
			baseline, counts = got, current
			continue
		}
		if got.key != baseline.key || !reflect.DeepEqual(got.weights, baseline.weights) || current != counts {
			t.Fatalf("trial=%d key=%s want=%s counts=%v want=%v weights=%v want=%v", trial, got.key, baseline.key, current, counts, got.weights, baseline.weights)
		}
	}
}

func TestCapacitySearchV2Steps(t *testing.T) {
	two := capacityThreeNodeSwap()
	two.plans, two.compared, two.snapshot.Nodes = two.plans[:2], two.compared[:2], two.snapshot.Nodes[:2]
	two.ws[0].rate, two.plans[0][1].after = 19, 15
	cover := capacityThreeNodeSwap()
	cover.ws[0].rate = 45
	countRecovery := capacityThreeNodeSwap()
	countRecovery.settings.MaxInstances = 2
	priced := e5Planner(map[int]float64{0: 60, 1: 15, 2: 1.2}, evaluationScaled(2, 0), e5Start, nil)
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
					step.ComparisonWindow != time.Duration(p.period*1e9) {
					t.Fatalf("invalid start/window: %+v", step)
				}
				pricedOrigin = pricedOrigin || step.Start == "priced" && step.K == 0
				if step.Start == capacitySearchStartKeep && step.K > 0 {
					found = found || step.Phase == tc.phase && step.K == tc.k
					lostFeasibility = lostFeasibility || previous.Feasible && !step.Feasible
				}
				if step.K > 0 && len(p.plans) == 1 && (step.Evaluated != 1 ||
					!slices.Equal(step.ChangedNodes, []NodeReference{{UID: "n0"}})) {
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
