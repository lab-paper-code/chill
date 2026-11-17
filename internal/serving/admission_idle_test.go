package serving

import (
	"fmt"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func idleTestState(id string, d *Definition) firstfloor.RequestState {
	return firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: id, WorkloadUID: d.UID, Definition: d.Identity(), AdmissionPolicy: string(d.Admission.Policy), SLO: firstfloor.SLO{Percentile: d.SLO.Percentile, TargetMS: float64(d.SLO.TargetLatency) / 1e6}}, Phase: phaseQueued}
}

// The executor's choice, the gateway's prediction and the replay agree on one
// candidate at a single executor of the fixture's 10 ms batch.
func TestIdleRuleAgreesInExecutorGatewayAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slo    time.Duration
		age    time.Duration // candidate age at arrival
		ahead  bool          // a request with a later deadline is queued ahead
		policy AdmissionPolicy
		fixed  bool
		// outcome is the replay outcome; the executor and the gateway take the
		// candidate unless it is rejected.
		outcome string
	}{
		{name: "idle late", slo: time.Millisecond, policy: AdmissionPolicyReject, outcome: schedulerOutcomeLate},
		{name: "idle on time", slo: time.Second, policy: AdmissionPolicyReject, outcome: schedulerOutcomeOnTime},
		{name: "queued ahead", slo: time.Millisecond, ahead: true, policy: AdmissionPolicyReject, outcome: schedulerOutcomeRejected},
		{name: "expired", slo: time.Millisecond, age: 2 * time.Millisecond, policy: AdmissionPolicyReject, outcome: schedulerOutcomeRejected},
		{name: "delay idle late", slo: time.Millisecond, policy: AdmissionPolicyDelay, outcome: schedulerOutcomeLate},
		{name: "fixed batch late", slo: time.Millisecond, policy: AdmissionPolicyReject, fixed: true, outcome: schedulerOutcomeLate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, snap := schedulerFixture()
			settings.AutonomousWorkloads = nil
			snap.Arrivals = nil
			if tc.fixed {
				snap.Instances[0].FixedBatchSize = 1
			}
			d := snap.Workloads[0]
			d.SLO.TargetLatency, d.Admission.Policy = tc.slo, tc.policy
			ahead := *d
			ahead.UID, ahead.SLO.TargetLatency, ahead.Admission.Policy = "ahead", time.Second, AdmissionPolicyReject
			candidate := idleTestState("candidate", d)
			candidate.Context.SenderElapsedMS = float64(tc.age) / 1e6
			snap.Outstanding = []SchedulerRequest{{ID: "candidate", Definition: d, ReceivedAt: snap.At.Add(-tc.age), Elapsed: tc.age, Phase: phasePending}}
			var observed []firstfloor.RequestState
			if tc.ahead {
				observed = append(observed, idleTestState("ahead", &ahead))
				zero := 0.0
				snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: "ahead", Definition: &ahead, ReceivedAt: snap.At, Phase: phaseQueued, Instance: "i1", NativeSenderElapsedMS: &zero})
			}
			policy := schedulerPolicy(snap.Instances[0])

			choice := firstfloor.ChooseBatch(policy, append(observed, candidate), new(int64))
			executorTakes := choice.Size == 1 && choice.WorkloadUID == d.UID

			estimate := EstimateAdmission(AdmissionInput{Policy: policy, Observed: observed, LastServiceEndAgeNanos: new(int64), Candidate: candidate})
			onTime, idle := admissionCandidateAvailable(estimate, false, tc.slo-tc.age)
			// The route has this one executor.
			gatewayTakes := estimate.FixedBatch || estimate.Known && (onTime || idle)

			e := schedulerRequireEstimate(t, schedulerKeepForTest(t, settings, snap), d.UID)
			taken := tc.outcome != schedulerOutcomeRejected
			for _, outcome := range []string{schedulerOutcomeOnTime, schedulerOutcomeLate, schedulerOutcomeRejected} {
				want := 0
				if outcome == tc.outcome {
					want = 1
				}
				if e.PredictedOutcomes[outcome] != want {
					t.Fatalf("replay %s=%d, want %d: %+v", outcome, e.PredictedOutcomes[outcome], want, e)
				}
			}
			if executorTakes != taken || gatewayTakes != taken {
				t.Fatalf("executor takes=%t gateway takes=%t, want %t: choice=%+v estimate=%+v", executorTakes, gatewayTakes, taken, choice, estimate)
			}
		})
	}
}

// The gateway judges idle when the candidate arrives. A late candidate behind
// running or queued work is rejected even when the projection reaches its
// batch with nothing else left: admitting it makes the executor drop the
// request queued before it.
func TestBusyExecutorRejectsLateCandidateAtGateway(t *testing.T) {
	_, snap := schedulerFixture()
	d := snap.Workloads[0]
	d.SLO.TargetLatency = 15 * time.Millisecond
	policy := schedulerPolicy(snap.Instances[0])
	running := func(elapsed time.Duration) firstfloor.RequestState {
		q := idleTestState("r1", d)
		exchange, gap := int64(elapsed), int64(0)
		q.Phase, q.BatchID, q.BatchSize, q.ExchangeElapsedNanos, q.PrecedingIdleNanos = phaseRunning, 1, 1, &exchange, &gap
		return q
	}
	queued := idleTestState("r2", d)
	queued.ElapsedMS, queued.QueueMS = 2, 2
	for _, tc := range []struct {
		name       string
		observed   []firstfloor.RequestState
		completion time.Duration
	}{
		{name: "running", observed: []firstfloor.RequestState{running(2 * time.Millisecond)}, completion: 18 * time.Millisecond},
		// r1 ends at 6 ms; neither r2 nor the candidate can finish in time, so
		// the projected executor drops r2 and runs the candidate alone.
		{name: "running and queued", observed: []firstfloor.RequestState{running(4 * time.Millisecond), queued}, completion: 16 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			estimate := EstimateAdmission(AdmissionInput{Policy: policy, Observed: tc.observed, LastServiceEndAgeNanos: new(int64), Candidate: idleTestState("candidate", d)})
			if onTime, idle := admissionCandidateAvailable(estimate, false, d.SLO.TargetLatency); !estimate.Known || estimate.Completion != tc.completion || estimate.Idle || onTime || idle {
				t.Fatalf("busy executor admitted a late candidate: %+v", estimate)
			}
		})
	}
	// That drop is what admitting the candidate costs: at r1's end r2 has 7 ms
	// and the candidate 9 ms of SLO left, and the executor drops r2.
	r2, r3 := queued, idleTestState("r3", d)
	r2.ElapsedMS, r2.QueueMS, r3.ElapsedMS, r3.QueueMS = 8, 8, 6, 6
	if choice := firstfloor.ChooseBatch(policy, []firstfloor.RequestState{r2, r3}, new(int64)); choice.Reason != firstfloor.OutcomeDeadlineDrop || choice.RequestID != "r2" {
		t.Fatalf("executor choice behind the late admission: %+v", choice)
	}
}

// Pairs of arrivals 2 ms apart every 11 ms on a 10 ms executor with a 15 ms
// SLO: the first of each pair finds the executor idle and runs on time, the
// second arrives while it runs and is rejected.
func TestReplayBusyExecutorRejectsSecondOfPair(t *testing.T) {
	settings, snap := schedulerFixture()
	settings.AutonomousWorkloads = nil
	settings.ObservationWindow, settings.ComparisonPeriod = 99*time.Millisecond, 99*time.Millisecond
	d := snap.Workloads[0]
	d.SLO.TargetLatency = 15 * time.Millisecond
	start := snap.ObservationEnd.Add(-settings.ObservationWindow)
	for k := range 9 {
		for j, offset := range []time.Duration{0, 2 * time.Millisecond} {
			snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprintf("a%d-%d", k, j), At: start.Add(time.Duration(k)*11*time.Millisecond + offset), Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID})
		}
	}
	e := schedulerRequireEstimate(t, schedulerKeepForTest(t, settings, snap), d.UID)
	if e.PredictedOutcomes[schedulerOutcomeOnTime] != 9 || e.PredictedOutcomes[schedulerOutcomeRejected] != 9 || e.PredictedOutcomes[schedulerOutcomeLate] != 0 {
		t.Fatalf("busy executor admitted late work: %+v", e.PredictedOutcomes)
	}
}

// A route of a 30 ms and a 70 ms executor with equal weights and a 50 ms SLO
// sends every arrival to the fast executor while it is on time, and to the idle
// slow one only when the fast one is busy.
func TestReplayRoutePrefersOnTimeExecutor(t *testing.T) {
	for _, tc := range []struct {
		name         string
		offsets      []time.Duration
		onTime, late int
	}{
		{name: "spaced", offsets: []time.Duration{0}, onTime: 5},
		{name: "pairs", offsets: []time.Duration{0, time.Millisecond}, onTime: 5, late: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, snap := schedulerFixture()
			settings.AutonomousWorkloads = nil
			settings.ObservationWindow, settings.ComparisonPeriod = time.Second, time.Second
			d := snap.Workloads[0]
			d.SLO.TargetLatency = 50 * time.Millisecond
			fast := snap.Instances[0]
			slow := poolsInstance(fast, "slow", poolsAddNode(&snap, "slow", map[string]float64{"fixed": 10}), "fixed", fast.WorkloadUID)
			snap.Instances = append(snap.Instances, slow)
			capacityServiceRatesForTest(&snap, map[string]float64{fast.Profile.ID: 1 / .03, slow.Profile.ID: 1 / .07})
			snap.Routing = map[string]map[string]int32{d.UID: {fast.ID: schedulerRouteUnits / 2, slow.ID: schedulerRouteUnits / 2}}
			start := snap.ObservationEnd.Add(-settings.ObservationWindow)
			for k := range 5 {
				for j, offset := range tc.offsets {
					snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: fmt.Sprintf("a%d-%d", k, j), At: start.Add(time.Duration(k)*200*time.Millisecond + offset), Definition: d, Accepted: true, Compute: true, WorkloadUID: d.UID})
				}
			}
			e := schedulerRequireEstimate(t, schedulerKeepForTest(t, settings, snap), d.UID)
			if e.PredictedOutcomes[schedulerOutcomeOnTime] != tc.onTime || e.PredictedOutcomes[schedulerOutcomeLate] != tc.late || e.PredictedOutcomes[schedulerOutcomeRejected] != 0 {
				t.Fatalf("route outcomes %+v, want onTime=%d late=%d", e.PredictedOutcomes, tc.onTime, tc.late)
			}
		})
	}
}

// The gateway applies the same route rule: a 10 ms and a 500 ms executor with
// equal weights and a 15 ms SLO.
func TestGatewayRoutePrefersOnTimeExecutor(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	fast := r.instances[wanted.ID].ReadyInstance
	slow := fast
	p := *fast.Profile
	p.ID = "slow"
	p.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{float64(500 * time.Millisecond)})}
	slow.State.ID, slow.State.Profile, slow.Profile = "a-slow", p.ID, &p
	slow.State.Native = runtimeTestNative(&p)
	slow.State.ObservedAt = time.Now()
	if err := r.InstallInstances([]ReadyInstance{slow, fast}); err != nil {
		t.Fatal(err)
	}
	w := r.workloads["w1"]
	d := *w.definition
	d.SLO.TargetLatency = 15 * time.Millisecond
	choose := func(id string) string {
		now := time.Now()
		c := &Call{receipt: Receipt{ID: id, UID: d.UID, ReceivedAt: now, Deadline: now.Add(time.Second)}, definition: &d, identity: w.identity}
		r.mu.Lock()
		defer r.mu.Unlock()
		choice := r.chooseLocked(w.route, c, now)
		if choice.entry == nil {
			t.Fatalf("no executor for %s: %+v", id, choice)
		}
		return choice.entry.instance.State.ID
	}
	for j := range 4 {
		if got := choose(fmt.Sprint("idle-", j)); got != wanted.ID {
			t.Fatalf("arrival %d went to %s while the fast executor is on time", j, got)
		}
	}
	// A request with an earlier deadline queued on the fast executor leaves the
	// candidate 20 ms there; only the idle slow executor takes it.
	ahead := idleTestState("ahead", &d)
	ahead.ElapsedMS, ahead.QueueMS = 1, 1
	r.mu.Lock()
	native := r.instances[wanted.ID].State.Native
	native.Requests = append(native.Requests, ahead)
	r.mu.Unlock()
	if got := choose("busy"); got != slow.State.ID {
		t.Fatalf("busy fast executor: candidate went to %s, want the idle slow executor", got)
	}
}
