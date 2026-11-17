package serving

import (
	"fmt"
	"math"
	"testing"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func capacitySupplyFixture() (*capacityPlanner, *capacityRoundsExact) {
	p := capacityDeltaTiny([]float64{50}, 40)
	p.index = map[string]int{"w": 0}
	return p, newCapacityRoundsExact(newCapacityExact(p))
}

// Only a shortfall, a missed SLO with part of the population uncompleted,
// measures throughput: c_w = completed rate / profile throughput, only lowered,
// and every surrogate throughput column of w is scaled by it.
func TestCapacitySupplyCorrectionOnlyFromShortfall(t *testing.T) {
	p, e := capacitySupplyFixture()
	observe := func(score FeedbackWorkloadScore) bool {
		score.UID, score.Population = "w", 100
		res := capacityResult{choice: []int{0}, feedback: []FeedbackWorkloadScore{score}}
		p.supplyRatios(res, e.ratios[0])
		return e.lowerSupply()
	}
	if observe(FeedbackWorkloadScore{OnTime: 90, Satisfied: true}) || observe(FeedbackWorkloadScore{Late: 100}) || p.supplyCorrection != nil {
		t.Fatalf("satisfied or fully completed replay corrected supply: %v", p.supplyCorrection)
	}
	if !observe(FeedbackWorkloadScore{OnTime: 60, Late: 20, Rejected: 20}) || math.Abs(p.supplyCorrection[0]-.64) > 1e-12 { // 0.8*40/50
		t.Fatalf("want c=0.64: %v", p.supplyCorrection)
	}
	if observe(FeedbackWorkloadScore{OnTime: 90, Rejected: 10}) || math.Abs(p.supplyCorrection[0]-.64) > 1e-12 {
		t.Fatalf("higher ratio raised c or reported a change: %v", p.supplyCorrection)
	}
	tab := p.planTable()
	q := tab.byPlan[0][0]
	if math.Abs(tab.routingRate[q][0]-32) > 1e-9 || math.Abs(tab.retainedRoutingRate[q][0]-32) > 1e-9 ||
		math.Abs(tab.routingRateServiceNanos[q][0]-32*20e6) > 1e-3 || math.Abs(tab.loadFinal[q][0][0].throughput-32) > 1e-9 {
		t.Fatalf("table not scaled by c: rate=%v retained=%v", tab.routingRate[q], tab.retainedRoutingRate[q])
	}
}

// A replay the planner does not admit did not run the candidate as planned:
// an invalid replay (its InvalidReasons are the exclusion) or an unrouted
// prepare lowers no supply through the exact port.
func TestCapacityUnadmittedReplayMeasuresNoThroughput(t *testing.T) {
	p, e := capacitySupplyFixture()
	short := []FeedbackWorkloadScore{{UID: "w", Population: 100, OnTime: 10, Rejected: 90}}
	for _, res := range []capacityResult{
		{choice: []int{0}, feedback: short, exclusion: "TemporaryReadinessResourceInfeasible"},
		{choice: []int{0}, feedback: short, unroutedPrepare: true},
	} {
		e.Observe(res, 0)
		if p.admits(&res) || e.lowerSupply() {
			t.Fatalf("unadmitted replay lowered supply: %v", p.supplyCorrection)
		}
	}
	e.Observe(capacityResult{choice: []int{0}, feedback: short}, 1)
	if !e.lowerSupply() {
		t.Fatal("admitted shortfall must lower supply")
	}
}

// A reset inside a later round (v1 fallback) clears only that round's
// observations; earlier rounds stay in the decision record.
func TestCapacityRoundsResetKeepsEarlierObservations(t *testing.T) {
	p, e := capacitySupplyFixture()
	e.Observe(capacityResult{choice: []int{0}, key: "a", exclusion: "NoRoute:w"}, 0)
	e.endRound()
	e.ResetObservations()
	e.record(protocol.Diagnostics{Strategy: protocol.V2, Fallback: protocol.V1})
	if p.searched.Excluded["NoRoute:w"] != 1 {
		t.Fatalf("earlier round's observation lost: %v", p.searched.Excluded)
	}
}

func TestCapacityRoundsStopWithoutImprovement(t *testing.T) {
	outcomes := []float64{.5, .3, .3, .1}
	runs := 0
	run := func() (protocol.Outcome[capacityResult], error) {
		res := capacityResult{excesses: []float64{outcomes[runs]}}
		runs++
		return protocol.Outcome[capacityResult]{Value: res, Found: true}, nil
	}
	best, selected, err := capacityRounds(run, func() bool { return true })
	if err != nil || runs != 3 || selected != 1 || best.Value.excesses[0] != .3 {
		t.Fatalf("want stop after the non-improving third round: runs=%d selected=%d best=%v err=%v", runs, selected, best.Value.excesses, err)
	}
}

// With a feasible best, lowered supply regenerates and a feasible result with
// lower energy against keep replaces the best; without lowered supply the
// first round stands.
func TestCapacityRoundsRegenerateAfterFeasibleBest(t *testing.T) {
	joules := []float64{100, 90, 95}
	runs := 0
	run := func() (protocol.Outcome[capacityResult], error) {
		res := capacityResult{feasible: true, joules: joules[runs], key: fmt.Sprint(runs)}
		runs++
		return protocol.Outcome[capacityResult]{Value: res, Found: true}, nil
	}
	best, selected, err := capacityRounds(run, func() bool { return true })
	if err != nil || runs != 3 || selected != 1 || best.Value.joules != 90 {
		t.Fatalf("want the cheaper feasible second round, stopped by the third: runs=%d selected=%d best=%v err=%v", runs, selected, best.Value.joules, err)
	}
	runs = 0
	best, selected, err = capacityRounds(run, func() bool { return false })
	if err != nil || runs != 1 || selected != 0 || best.Value.joules != 100 {
		t.Fatalf("want the first round alone when no correction fell: runs=%d selected=%d best=%v err=%v", runs, selected, best.Value.joules, err)
	}
}
