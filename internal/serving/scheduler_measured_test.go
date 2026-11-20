package serving

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// measuredFixture serves w1 at 20 req/s on one instance the replay judges
// feasible, with two expansion options (capacityE1Fixture) and a 4 s window.
// Its first 3 s, the arrivals that had the whole 1 s SLO target to report,
// hold 60 arrivals, enough to identify the 95th percentile (59).
func measuredFixture() (SchedulerSettings, SchedulerSnapshot) {
	s, snap, _ := capacityE1Fixture()
	s.ObservationWindow = 4 * time.Second
	snap.Workloads[0].SLO.Percentile = 95
	capacityLoad(s, &snap, snap.Workloads[0], 20)
	return s, snap
}

// measuredReports sets every arrival's client report: the first failed arrivals
// fail, the rest complete on time.
func measuredReports(snap *SchedulerSnapshot, failed int) {
	for j := range snap.Arrivals {
		snap.Arrivals[j].ClientOutcome, snap.Arrivals[j].ClientElapsed = outcomeCompleted, 10*time.Millisecond
		if j < failed {
			snap.Arrivals[j].ClientOutcome = clientOutcomeHTTPError
		}
	}
}

// measuredScoreOf is c's score of w1.
func measuredScoreOf(t *testing.T, c *SchedulerCandidate) FeedbackWorkloadScore {
	t.Helper()
	if c == nil {
		t.Fatal("missing candidate")
	}
	at := slices.IndexFunc(c.Feedback, func(score FeedbackWorkloadScore) bool { return score.UID == "w1" })
	if at < 0 {
		t.Fatalf("missing feedback for w1: %+v", c.Feedback)
	}
	return c.Feedback[at]
}

// servingOfW1 is how c serves w1.
func servingOfW1(c *SchedulerCandidate) []SchedulerServingShare {
	return servingShares(c.Routing["w1"], func(id string) (SchedulerServingShare, bool) {
		at := slices.IndexFunc(c.Instances, func(i SchedulerInstance) bool { return i.ID == id })
		if at < 0 {
			return SchedulerServingShare{}, false
		}
		return servingOption(c.Instances[at]), true
	})
}

// The replay of the running configuration meets the SLO; its measured window
// does not. The identical configuration is judged by the measurement too, so
// the decision is no Keep: it adds capacity. The decision returns the miss.
func TestMeasuredInfeasibleKeepIsNotKept(t *testing.T) {
	s, snap := measuredFixture()
	if d := capacityDecideForTest(t, s, snap); d.Status != SchedulerStatusKeep || measuredScoreOf(t, &d.Keep).Measured != nil || len(d.Measurements) != 0 {
		t.Fatalf("unreported window must keep the replay-feasible configuration: status=%s keep=%+v", d.Status, d.Keep.Feedback)
	}
	measuredReports(&snap, 6) // 54 of 60 on time < ⌈0.95·60⌉ = 57
	d := capacityDecideForTest(t, s, snap)
	keep := measuredScoreOf(t, &d.Keep)
	if keep.Measured == nil || keep.Measured.Population != 60 || keep.Measured.OnTime != 54 || keep.Measured.Satisfied || !keep.Satisfied {
		t.Fatalf("keep must record the replayed prediction and the measured verdict: %+v %+v", keep, keep.Measured)
	}
	if d.Keep.Status != SchedulerStatusOverload || d.Status == SchedulerStatusKeep || d.Target == nil || len(d.Target.Instances) < 2 {
		t.Fatalf("measured infeasible keep ended in %s: target=%+v", d.Status, d.Target)
	}
	if measuredScoreOf(t, d.Target).Measured != nil {
		t.Fatalf("an added instance changes w1's serving; the replay must judge it: %+v", d.Target.Feedback)
	}
	if len(d.Measurements) != 1 || d.Measurements[0].OnTimeRate != 18 || !sameSet(d.Measurements[0].Serving, servingOfW1(&d.Keep)) ||
		d.Measurements[0].Since != nil || d.Measurements[0].Population != 0 || d.Measurements[0].Serving[0].Weight != 0 {
		t.Fatalf("the decision must return the set that missed and its on-time rate, nothing else: %+v", d.Measurements)
	}
}

// A late completion is no on-time completion, and the on-time rate counts
// on-time completions only: 56 of 60 in 3 s.
func TestMeasuredLateCompletionIsNotOnTime(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 0)
	for j := range snap.Arrivals[:4] {
		snap.Arrivals[j].ClientElapsed = 2 * snap.Workloads[0].SLO.TargetLatency
	}
	m := capacityPlannerForTest(t, s, snap).ws[0].window
	if m == nil || m.Population != 60 || m.OnTime != 56 || m.Late != 4 || m.Satisfied || m.OnTimeRate != 56.0/3 {
		t.Fatalf("4 late of 60 must miss at 56/3 req/s on time: %+v", m)
	}
}

// An arrival older than the target with neither a client report nor a server
// outcome can no longer finish on time: it is a miss. One the server finished
// has an unknown client outcome and is left out.
func TestMeasuredUnreportedOldArrivalIsAMiss(t *testing.T) {
	s, snap := measuredFixture()
	capacityLoad(s, &snap, snap.Workloads[0], 40) // 120 arrivals over 3 s
	measuredReports(&snap, 0)
	for j := range snap.Arrivals[:7] {
		snap.Arrivals[j].ClientOutcome, snap.Arrivals[j].Outcome = "", ""
	}
	if m := capacityPlannerForTest(t, s, snap).ws[0].window; m == nil || m.Population != 120 || m.OnTime != 113 || m.Satisfied {
		t.Fatalf("7 unfinished arrivals older than the target must count as misses (113 < 114 of 120): %+v", m)
	}
	for j := range snap.Arrivals[:7] {
		snap.Arrivals[j].Outcome = outcomeCompleted
	}
	if m := capacityPlannerForTest(t, s, snap).ws[0].window; m == nil || m.Population != 113 || !m.Satisfied {
		t.Fatalf("arrivals the server finished without a client report must be left out: %+v", m)
	}
}

// A routed instance that is not routable for w is no settled serving: there is
// no window.
func TestMeasuredWindowNeedsEveryRoutedInstanceRoutable(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 6)
	snap.Instances[0].Routable = map[string]bool{"w1": false}
	if m := capacityPlannerForTest(t, s, snap).ws[0].window; m != nil {
		t.Fatalf("an unroutable routed instance must leave no window: %+v", m)
	}
}

// The window restarts at the end of the last action: arrivals before it, here
// all failed, do not judge the configuration the action produced.
func TestMeasuredWindowRestartsAfterAction(t *testing.T) {
	s, snap := measuredFixture()
	capacityLoad(s, &snap, snap.Workloads[0], 40) // 120 arrivals over 3 s
	measuredReports(&snap, 40)
	ended := snap.Arrivals[40].At
	snap.Actions = []Action{{ID: "route", Kind: schedulerActionRoute, Status: schedulerActionComplete, EndedAt: &ended}}
	p := capacityPlannerForTest(t, s, snap)
	m := p.evaluateCurrentKeep(p.periodSeconds).feedback[0].Measured
	if m == nil || !m.Since.Equal(ended) || m.Population != 80 || m.OnTime != 80 || !m.Satisfied {
		t.Fatalf("window must start at the action's end: %+v", m)
	}
	snap.Actions[0].Status, snap.Actions[0].EndedAt = phaseRunning, nil
	p = capacityPlannerForTest(t, s, snap)
	if m := p.evaluateCurrentKeep(p.periodSeconds).feedback[0].Measured; m != nil {
		t.Fatalf("a running action leaves no window: %+v", m)
	}
}

// A measured miss corrects the surrogate supply by the on-time rate the set
// delivered (18 req/s: 54 in 3 s), not by its on-time share of the planned
// demand, which 30 queued requests raise above what was offered (20 + 30/3).
func TestMeasuredShortfallIsTheDeliveredRate(t *testing.T) {
	s, snap := measuredFixture()
	for j := range 30 {
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: fmt.Sprint("q", j), Definition: snap.Workloads[0], ReceivedAt: snap.At, Phase: phasePending})
	}
	measuredReports(&snap, 6)
	p := capacityPlannerForTest(t, s, snap)
	got := p.shortfalls(p.evaluateCurrentKeep(p.periodSeconds))
	if len(got) != 1 || got[0].completed != 18 {
		t.Fatalf("measured shortfall must be the delivered on-time rate 18, not 54/60 of 30: %+v", got)
	}
}

// Fewer reports than identify the percentile leave the replay's verdict.
func TestMeasuredTooFewReportsFallToReplay(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 6)
	for j := range snap.Arrivals[:2] {
		snap.Arrivals[j].ClientOutcome = "" // 58 reported, one below the floor
	}
	d := capacityDecideForTest(t, s, snap)
	if keep := measuredScoreOf(t, &d.Keep); keep.Measured != nil || d.Status != SchedulerStatusKeep {
		t.Fatalf("58 reports must not judge the 95th percentile: status=%s keep=%+v", d.Status, keep)
	}
}

// The window's measurement judges only a result serving w1 with the same
// options and traffic shares; scaling every weight is the same serving. An
// added instance is replay-judged. A shifted share over the same set is that
// set: the remembered miss covers it (18 req/s delivered, 19 needed) at the
// result's own population, and its rate corrects no supply.
func TestMeasuredVerdictOnlyForTheSameServing(t *testing.T) {
	s, snap := measuredFixture()
	snap.Instances = append(snap.Instances, poolsInstance(snap.Instances[0], "i2", NodeReference{Name: "node-lut", UID: "lut"}, "fixed", "w1"))
	measuredReports(&snap, 6)
	p := capacityPlannerForTest(t, s, snap)
	keep := p.evaluateCurrentKeep(p.periodSeconds)
	if keep.feedback[0].Measured == nil || keep.feasible {
		t.Fatalf("keep must be judged by its measured window: %+v", keep.feedback)
	}
	added := p.evaluate(capacityChoiceForTest(t, p, map[string]string{"genesis3": "profile@genesis3-fixed/b1/w1"}), 10.7)
	if added.feedback[0].Measured != nil {
		t.Fatalf("an added instance must be replay-judged: %+v", added.feedback)
	}
	route := func(i1, i2 int32) capacityResult {
		return p.assessResult(p.evaluateEnergyForRoutes(make([]int, len(p.plans)), 0, nil, p.periodSeconds, map[string]map[string]int32{"w1": {"i1": i1, "i2": i2}}))
	}
	if shifted := route(2, 1).feedback[0]; shifted.Measured == nil || shifted.Measured.Since != nil || shifted.Measured.OnTimeRate != 18 ||
		shifted.Measured.Population != shifted.Population || shifted.Measured.OnTime != int(18*float64(shifted.Population)/20) || shifted.Measured.Satisfied {
		t.Fatalf("a shifted share over the set that missed must take its remembered miss at its own population: %+v %+v", shifted, shifted.Measured)
	}
	if _, short := p.shortfall(0, route(2, 1).feedback[0]); short {
		t.Fatal("a remembered miss must not correct the surrogate supply: its rate is another window's")
	}
	if scaled := route(1000, 1000); !scaled.keep || scaled.feedback[0].Measured == nil || scaled.feedback[0].Measured.Since == nil || scaled.feedback[0].Measured.Population != 60 {
		t.Fatalf("scaled weights are the running serving, judged by its window: %+v", scaled.feedback)
	}
}

// measuredMissOfA is the miss the fixture's running serving A (i1 alone)
// measures at 20 req/s: 54 of 60 on time over 3 s, 18 req/s delivered.
func measuredMissOfA(t *testing.T) []SchedulerMeasurement {
	t.Helper()
	s, snap := measuredFixture()
	measuredReports(&snap, 6)
	misses := capacityDecideForTest(t, s, snap).Measurements
	if len(misses) != 1 || misses[0].OnTimeRate != 18 {
		t.Fatalf("A must miss, delivering 18 req/s on time: %+v", misses)
	}
	return misses
}

// measuredB runs B = A + q2 on genesis3, whose removal has a measured cost and
// saves q2's load, with every report on time at rate req/s, and carries the
// remembered misses.
func measuredB(t *testing.T, rate float64, misses []SchedulerMeasurement) (SchedulerSettings, SchedulerSnapshot) {
	t.Helper()
	s, snap := measuredFixture()
	capacityLoad(s, &snap, snap.Workloads[0], rate)
	q2 := snap.Options[slices.IndexFunc(snap.Options, func(o SchedulerOption) bool { return o.Instance.ID == "q2" })].Instance
	snap.Instances = append(snap.Instances, q2)
	snap.Routing = map[string]map[string]int32{"w1": {"i1": 1, "q2": 1}}
	snap.Costs = append(snap.Costs, poolsCost(schedulerActionRemove, q2.Node, schedulerState(q2), "unloaded", "fixed"))
	measuredReports(&snap, 0)
	snap.Measurements = misses
	return s, snap
}

// servesAsA reports whether c serves w1 on A's profile alone, at any cap.
func servesAsA(c *SchedulerCandidate) bool {
	if c == nil {
		return false
	}
	_, snap := measuredFixture()
	serving := servingOfW1(c)
	return len(serving) == 1 && serving[0].ProfileID == servingOption(snap.Instances[0]).ProfileID
}

// cappedMiss is A's miss as if measured with A's profile at batch cap maxBatch.
func cappedMiss(t *testing.T, maxBatch int) []SchedulerMeasurement {
	misses := measuredMissOfA(t)
	misses[0].Serving = slices.Clone(misses[0].Serving)
	misses[0].Serving[0].MaxBatch = maxBatch
	return misses
}

// B measured fine at the demand A missed at. A is A's own set, so it is
// covered and needs 19 req/s on time (57 of 60 at 20 req/s), more than the 18
// A delivered: the decision does not go back to A. Without the fact the
// replay calls A feasible and cheaper.
func TestMeasuredMissKeepsTheScheduleFromReturning(t *testing.T) {
	s, snap := measuredB(t, 20, nil)
	if d := capacityDecideForTest(t, s, snap); !servesAsA(d.Target) {
		t.Fatalf("fixture: without the miss the replay returns to A: status=%s target=%+v", d.Status, d.Target)
	}
	s, snap = measuredB(t, 20, measuredMissOfA(t))
	d := capacityDecideForTest(t, s, snap)
	if servesAsA(d.Target) || len(d.Measurements) != 1 {
		t.Fatalf("A missed at this demand; the decision returned to it: status=%s target=%+v measurements=%+v", d.Status, d.Target, d.Measurements)
	}
}

// A set that missed with a larger cap on A's profile covers A, the weaker
// variant. A larger cap than the one that missed is not covered, nor is
// another profile, an added instance or a second instance of a profile the set
// had once: the replay judges them.
func TestMeasuredMissCoversOnlyWeakerVariants(t *testing.T) {
	s, snap := measuredB(t, 20, cappedMiss(t, 2))
	if d := capacityDecideForTest(t, s, snap); servesAsA(d.Target) {
		t.Fatalf("A (cap 1) is weaker than the set that missed at cap 2 and must be covered: status=%s target=%+v", d.Status, d.Target)
	}
	fact := SchedulerMeasurement{Serving: []SchedulerServingShare{{ProfileID: "p", MaxBatch: 4, Weight: 1}, {ProfileID: "q", MaxBatch: 1, Weight: 1}}}
	for _, c := range []struct {
		options []SchedulerServingShare
		covered bool
	}{
		{[]SchedulerServingShare{{ProfileID: "p", MaxBatch: 4}}, true},
		{[]SchedulerServingShare{{ProfileID: "p", MaxBatch: 1}, {ProfileID: "q", MaxBatch: 1}}, true},
		{[]SchedulerServingShare{{ProfileID: "p", MaxBatch: 16}}, false},
		{[]SchedulerServingShare{{ProfileID: "p", MaxBatch: 4}, {ProfileID: "r", MaxBatch: 1}}, false},
		{[]SchedulerServingShare{{ProfileID: "p", MaxBatch: 1}, {ProfileID: "p", MaxBatch: 4}}, false},
		{[]SchedulerServingShare{{ProfileID: "q", MaxBatch: 1}, {ProfileID: "q", MaxBatch: 1}}, false},
	} {
		if fact.covers(c.options) != c.covered {
			t.Fatalf("covers(%+v) = %v, want %v", c.options, !c.covered, c.covered)
		}
	}
}

// The remembered on-time rate only grows: a later window of the same set
// delivering less changes nothing, one delivering more raises it.
func TestMeasuredMissOnTimeRateDoesNotFall(t *testing.T) {
	_, snap := measuredFixture()
	snap.Measurements = measuredMissOfA(t)
	later := snap.Measurements[0]
	for _, c := range []struct{ rate, want float64 }{{9, 18}, {25, 25}} {
		later.OnTimeRate = c.rate
		if got := remember(snap, []SchedulerMeasurement{later}); len(got) != 1 || got[0].OnTimeRate != c.want {
			t.Fatalf("window at %g req/s: remembered %+v, want %g", c.rate, got, c.want)
		}
	}
}

// At 10 req/s A needs 9.5 req/s on time, less than the 18 it delivered: the
// miss does not apply and A is allowed again. The need is the SLO rank of the
// demand, not all of it: at 20 req/s A needs 19 (57 of 60), less than 19.5.
func TestMeasuredMissAllowsADemandItServed(t *testing.T) {
	s, snap := measuredB(t, 10, measuredMissOfA(t))
	if d := capacityDecideForTest(t, s, snap); !servesAsA(d.Target) {
		t.Fatalf("at 10 req/s A, which delivered 18, must be replay-judged: status=%s target=%+v", d.Status, d.Target)
	}
	miss := measuredMissOfA(t)
	miss[0].OnTimeRate = 19.5
	s, snap = measuredB(t, 20, miss)
	if d := capacityDecideForTest(t, s, snap); !servesAsA(d.Target) {
		t.Fatalf("at 20 req/s A needs 19 on time, less than the 19.5 it delivered: status=%s target=%+v", d.Status, d.Target)
	}
}

// A miss is about the profile and cap, not the instance: a re-created
// instance of A is covered by it.
func TestMeasuredMissCoversARecreatedInstance(t *testing.T) {
	s, snap := measuredFixture()
	// No reports: only the remembered miss can judge.
	snap.Instances[0].ID = "i1-recreated"
	snap.Routing = nil
	snap.Measurements = measuredMissOfA(t)
	p := capacityPlannerForTest(t, s, snap)
	if keep := p.evaluateCurrentKeep(p.periodSeconds); keep.feasible || keep.feedback[0].Measured == nil || keep.feedback[0].Measured.Satisfied {
		t.Fatalf("the re-created instance is A and must take A's miss: %+v", keep.feedback)
	}
}

// A re-measured profile has a new ID: the old profile's miss does not cover
// it. The miss stays while its Definition does.
func TestMeasuredMissDoesNotCoverARemeasuredProfile(t *testing.T) {
	s, snap := measuredFixture()
	snap.Measurements = measuredMissOfA(t)
	profile := *snap.Instances[0].Profile
	profile.ID = snap.Instances[0].Profile.ID + "-remeasured"
	for j := range snap.Options {
		if snap.Options[j].Instance.Profile.ID == snap.Instances[0].Profile.ID {
			snap.Options[j].Instance.Profile = &profile
		}
	}
	snap.Instances[0].Profile = &profile
	d := capacityDecideForTest(t, s, snap)
	if keep := measuredScoreOf(t, &d.Keep); keep.Measured != nil || d.Status != SchedulerStatusKeep || len(d.Measurements) != 1 {
		t.Fatalf("a miss of a retired profile must not judge its successor: status=%s keep=%+v measurements=%+v", d.Status, keep, d.Measurements)
	}
}

// A miss belongs to its Definition: a new generation (an SLO, admission or
// lifetime change) drops it.
func TestMeasuredMissEndsWithItsDefinition(t *testing.T) {
	s, snap := measuredFixture()
	snap.Measurements = measuredMissOfA(t)
	snap.Workloads[0].Generation++
	d := capacityDecideForTest(t, s, snap)
	if keep := measuredScoreOf(t, &d.Keep); keep.Measured != nil || d.Status != SchedulerStatusKeep || len(d.Measurements) != 0 {
		t.Fatalf("a miss of an earlier generation must not judge or survive: status=%s keep=%+v measurements=%+v", d.Status, keep, d.Measurements)
	}
}
