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

// The replay of the running configuration meets the SLO; its measured window
// does not. The identical configuration is judged by the measurement too, so
// the decision is no Keep: it adds capacity.
func TestMeasuredInfeasibleKeepIsNotKept(t *testing.T) {
	s, snap := measuredFixture()
	if d := capacityDecideForTest(t, s, snap); d.Status != SchedulerStatusKeep || measuredScoreOf(t, &d.Keep).Measured != nil {
		t.Fatalf("unreported window must keep the replay-feasible configuration: status=%s keep=%+v", d.Status, d.Keep.Feedback)
	}
	measuredReports(&snap, 10) // 50 of 60 on time: P(X <= 50) = 0.0007 for Binomial(60, 0.95), below 0.05/4
	d := capacityDecideForTest(t, s, snap)
	keep := measuredScoreOf(t, &d.Keep)
	if keep.Measured == nil || keep.Measured.Population != 60 || keep.Measured.OnTime != 50 || keep.Measured.Satisfied || !keep.Satisfied {
		t.Fatalf("keep must record the replayed prediction and the measured verdict: %+v %+v", keep, keep.Measured)
	}
	if d.Keep.Status != SchedulerStatusOverload || d.Status == SchedulerStatusKeep || d.Target == nil || len(d.Target.Instances) < 2 {
		t.Fatalf("measured infeasible keep ended in %s: target=%+v", d.Status, d.Target)
	}
	if measuredScoreOf(t, d.Target).Measured != nil {
		t.Fatalf("an added instance changes w1's serving; the replay must judge it: %+v", d.Target.Feedback)
	}
}

// A late completion is no on-time completion, and the on-time rate counts
// on-time completions only: 50 of 60 in 3 s.
func TestMeasuredLateCompletionIsNotOnTime(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 0)
	for j := range snap.Arrivals[:10] {
		snap.Arrivals[j].ClientElapsed = 2 * snap.Workloads[0].SLO.TargetLatency
	}
	m := capacityPlannerForTest(t, s, snap).ws[0].window
	if m == nil || m.Population != 60 || m.OnTime != 50 || m.Late != 10 || m.Satisfied || m.OnTimeRate != 50.0/3 {
		t.Fatalf("10 late of 60 must miss at 50/3 req/s on time: %+v", m)
	}
}

// An arrival older than the target with neither a client report nor a server
// outcome can no longer finish on time: it is a miss. One the server finished
// has an unknown client outcome and is left out.
func TestMeasuredUnreportedOldArrivalIsAMiss(t *testing.T) {
	s, snap := measuredFixture()
	capacityLoad(s, &snap, snap.Workloads[0], 40) // 120 arrivals over 3 s
	measuredReports(&snap, 0)
	for j := range snap.Arrivals[:20] {
		snap.Arrivals[j].ClientOutcome, snap.Arrivals[j].Outcome = "", ""
	}
	if m := capacityPlannerForTest(t, s, snap).ws[0].window; m == nil || m.Population != 120 || m.OnTime != 100 || m.Satisfied {
		t.Fatalf("20 unfinished arrivals older than the target must count as misses (100 of 120, significantly short): %+v", m)
	}
	for j := range snap.Arrivals[:20] {
		snap.Arrivals[j].Outcome = outcomeCompleted
	}
	if m := capacityPlannerForTest(t, s, snap).ws[0].window; m == nil || m.Population != 100 || !m.Satisfied {
		t.Fatalf("arrivals the server finished without a client report must be left out: %+v", m)
	}
}

// A routed instance that is not routable for w is no settled serving: there is
// no window.
func TestMeasuredWindowNeedsEveryRoutedInstanceRoutable(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 10)
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
// delivered (50 in 3 s), not by its on-time share of the planned demand, which
// 30 queued requests raise above what was offered (20 + 30/3).
func TestMeasuredShortfallIsTheDeliveredRate(t *testing.T) {
	s, snap := measuredFixture()
	for j := range 30 {
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: fmt.Sprint("q", j), Definition: snap.Workloads[0], ReceivedAt: snap.At, Phase: phasePending})
	}
	measuredReports(&snap, 10)
	p := capacityPlannerForTest(t, s, snap)
	got := p.shortfalls(p.evaluateCurrentKeep(p.periodSeconds))
	if len(got) != 1 || got[0].completed != 50.0/3 {
		t.Fatalf("measured shortfall must be the delivered on-time rate 50/3, not 50/60 of the planned demand: %+v", got)
	}
}

// Fewer reports than identify the percentile leave the replay's verdict.
func TestMeasuredTooFewReportsFallToReplay(t *testing.T) {
	s, snap := measuredFixture()
	measuredReports(&snap, 10)
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
// added instance, a shifted traffic share or a changed batch cap is
// replay-judged.
func TestMeasuredVerdictOnlyForTheSameServing(t *testing.T) {
	s, snap := measuredFixture()
	snap.Instances = append(snap.Instances, poolsInstance(snap.Instances[0], "i2", NodeReference{Name: "node-lut", UID: "lut"}, "fixed", "w1"))
	measuredReports(&snap, 10)
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
	if shifted := route(2, 1).feedback[0]; shifted.Measured != nil {
		t.Fatalf("shifted shares must be replay-judged: %+v", shifted)
	}
	if scaled := route(1000, 1000); !scaled.keep || scaled.feedback[0].Measured == nil || scaled.feedback[0].Measured.Since == nil || scaled.feedback[0].Measured.Population != 60 {
		t.Fatalf("scaled weights are the running serving, judged by its window: %+v", scaled.feedback)
	}
	capped := slices.Clone(p.ws[0].serving)
	capped[0].MaxBatch++
	if sameServing(capped, p.ws[0].serving) {
		t.Fatal("a changed batch cap is another serving: the replay must judge it")
	}
}

// A measurement needs the running serving settled for one control interval:
// from the last action's end to the end of the observation window. At 120
// req/s and a 100 ms target the stretch to measure holds enough reports
// either way, but a serving settled for 0.9 s, under the 1 s interval, has no
// measurement and leaves the replay's verdict; one settled for 1.1 s has one.
func TestMeasuredWindowNeedsOneControlInterval(t *testing.T) {
	s, snap := measuredFixture()
	snap.Workloads[0].SLO.TargetLatency = 100 * time.Millisecond
	capacityLoad(s, &snap, snap.Workloads[0], 120)
	measuredReports(&snap, 0)
	for _, c := range []struct {
		settled  time.Duration
		measured bool
	}{{900 * time.Millisecond, false}, {1100 * time.Millisecond, true}} {
		ended := snap.ObservationEnd.Add(-c.settled)
		snap.Actions = []Action{{ID: "route", Kind: schedulerActionRoute, Status: schedulerActionComplete, EndedAt: &ended}}
		if m := capacityPlannerForTest(t, s, snap).ws[0].window; (m != nil) != c.measured || m != nil && m.Population != 120 {
			t.Fatalf("a serving settled for %v: measurement %+v, want one: %v", c.settled, m, c.measured)
		}
	}
}

// The settled stretch is the whole serving time, not the part one target
// before the end that is measured: with the control interval as long as the
// observation window (4 s) and a 1 s target, a serving settled for the whole
// window has its measurement over the first 3 s.
func TestMeasurementExistsWithTheIntervalAsLongAsTheWindow(t *testing.T) {
	s, snap := measuredFixture()
	s.ControlInterval = s.ObservationWindow
	measuredReports(&snap, 0)
	if m := capacityPlannerForTest(t, s, snap).ws[0].window; m == nil || m.Population != 60 || !m.Satisfied {
		t.Fatalf("a serving settled for the whole window must be measured: %+v", m)
	}
}

// The measured verdict is the one-sided binomial test at the level the
// decisions that look at one window share, exact for the measurement's whole
// counts. At p95, a 60 s window and 10 s decisions (0.05/6), 3646 on time of
// 3839 (94.97 %) cannot be told from 95 % and meets the SLO; 426 of 581 does
// not. Through windowMeasurement in measuredFixture (a 4 s window and 1 s
// decisions, 0.05/4), 53 on time of 60 (P(X <= 53) = 0.030) meets it, which
// the unshared 0.05 would call a miss.
func TestMeasuredVerdictIsASignificantShortfall(t *testing.T) {
	s := SchedulerSettings{ObservationWindow: time.Minute, ControlInterval: 10 * time.Second}
	d := &Definition{SLO: SLO{Percentile: 95}}
	if schedulerOnTimeShort(s, d, 3839, 3646) || !schedulerOnTimeShort(s, d, 581, 426) {
		t.Fatal("3646 of 3839 must meet p95 and 426 of 581 must miss it")
	}
	s, snap := measuredFixture()
	measuredReports(&snap, 7)
	if m := capacityPlannerForTest(t, s, snap).ws[0].window; m == nil || m.Population != 60 || m.OnTime != 53 || !m.Satisfied {
		t.Fatalf("53 of 60 must meet p95 at 0.05/4: %+v", m)
	}
}
