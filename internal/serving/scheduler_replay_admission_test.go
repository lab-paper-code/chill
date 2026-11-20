package serving

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

func replayAdmissionState(t testing.TB) *schedulerReplayState {
	t.Helper()
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	state := &schedulerReplayState{settings: settings, snap: snapshot, c: SchedulerCandidate{Keep: true, Instances: snapshot.Instances, Nodes: snapshot.Nodes}, rng: rand.New(rand.NewPCG(23, 47)), horizon: float64(settings.ComparisonPeriod), routeCredits: map[string][]int64{}, out: schedulerReplayResult{samples: map[string][]float64{}, definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}}}
	state.initializeInstances()
	state.weights, state.available = make([]int32, len(state.instances)), make([]bool, len(state.instances))
	return state
}

func replayAdmissionPending(s *schedulerReplayState, id string, definition *Definition) *schedulerReplayRequest {
	request := &schedulerReplayRequest{source: SchedulerRequest{ID: id, Definition: definition, ReceivedAt: s.snap.At, Phase: phasePending}, identity: definition.Identity(), native: math.Inf(1), expires: float64(2 * time.Second)}
	s.requests, s.live = append(s.requests, request), append(s.live, request)
	s.out.definitions[request.identity] = definition
	return request
}

func TestReplayAdmissionSkipsExecutorAtLimit(t *testing.T) {
	s := replayAdmissionState(t)
	slow := s.instances[0]
	// 200 ms per request clears none within the fixture's 100 ms target, so
	// the limit is one batch, one request, which it already holds.
	profile := *slow.config.Profile
	profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{200e6})}
	slow.config.Profile, slow.policy = &profile, schedulerPolicy(SchedulerInstance{Profile: &profile, MaxBatch: 1})
	slow.outstanding = 1
	fast := *slow
	fast.config.ID, fast.config.Profile, fast.outstanding = "fast", s.snap.Instances[0].Profile, 0
	fast.policy = schedulerPolicy(fast.config)
	s.instances = append(s.instances, &fast)
	s.byID["fast"] = &fast
	s.weights, s.available = make([]int32, 2), make([]bool, 2)
	r := replayAdmissionPending(s, "candidate", s.snap.Workloads[0])
	s.routeRequests(context.Background())
	if !r.dispatched || r.source.Instance != "fast" || r.dropped {
		t.Fatalf("executor at its limit took a request: %+v", r)
	}
}

// An executor that is not ready leaves every request waiting until its
// lifetime; one at its limit rejects a Reject request and holds a Delay one.
func TestReplayAdmissionWaitsOrRejects(t *testing.T) {
	for _, cause := range []string{"not ready", "stopped", "at limit/Reject", "at limit/Delay"} {
		t.Run(cause, func(t *testing.T) {
			s := replayAdmissionState(t)
			i := s.instances[0]
			d := *s.snap.Workloads[0]
			switch cause {
			case "not ready":
				i.ready = false
			case "stopped":
				i.stopped = true
			case "at limit/Reject":
				i.outstanding = 10
			case "at limit/Delay":
				i.outstanding = 10
				d.Admission.Policy = AdmissionPolicyDelay
			}
			r := replayAdmissionPending(s, "candidate", &d)
			later := replayAdmissionPending(s, "later", &d)
			s.routeRequests(context.Background())
			if cause == "at limit/Reject" {
				if !r.dropped || !later.dropped || s.out.outcomes[r.identity][schedulerOutcomeRejected] != 2 {
					t.Fatalf("requests at the limit were not rejected: %+v %+v", r, later)
				}
				return
			}
			if r.dispatched || r.dropped || later.dispatched || later.dropped {
				t.Fatalf("a waiting request was dispatched or rejected: %+v", r)
			}
			s.now = r.expires
			s.expireRequests()
			s.completeExecution()
			if !r.expired || s.out.outcomes[r.identity][schedulerOutcomeTimeout] != 2 || s.out.outcomes[r.identity][schedulerOutcomeRejected] != 0 {
				t.Fatalf("waiting request did not retain its final deadline: %+v", r)
			}
		})
	}
}

func TestReplayAdmissionQueueLimitAndRejectedDemand(t *testing.T) {
	s := replayAdmissionState(t)
	d := s.snap.Workloads[0]
	d.Admission.MaxQueuedRequests = 2
	s.instances[0].ready = false
	for _, id := range []string{"first", "second", "new"} {
		replayAdmissionPending(s, id, d)
	}
	s.live, s.arrived = nil, 0
	s.admitArrivals(context.Background())
	if len(s.live) != 2 || s.out.outcomes[d.Identity()][schedulerOutcomeRejected] != 1 {
		t.Fatalf("queue limit must count waiting requests: %+v", s.out)
	}
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	snapshot.Arrivals = []SchedulerArrival{{ID: "rejected-demand", Definition: snapshot.Workloads[0], At: snapshot.ObservationEnd.Add(-settings.ObservationWindow), Compute: true, Accepted: false, Outcome: schedulerOutcomeRejected}}
	cohort, err := newFeedbackArrivalCohort(context.Background(), settings, snapshot, schedulerSearchPopulation(context.Background(), settings, snapshot).Loads)
	if err != nil || len(cohort.requests) != 1 || snapshot.Arrivals[0].Accepted {
		t.Fatalf("rejected compute demand disappeared or fabricated acceptance: %+v %v", cohort, err)
	}
}

func TestCapacityAdmissionChangesInvalidateRowKey(t *testing.T) {
	settings, snapshot := schedulerFixture()
	definition := snapshot.Workloads[0]
	option := snapshot.Options[0]
	baseline := NewCapacityInput(settings, capacityTestWindows, option, definition).Key()
	for _, change := range []string{"policy", "queue limit"} {
		frozen := *definition
		if change == "policy" {
			frozen.Admission.Policy = AdmissionPolicyDelay
		} else {
			frozen.Admission.MaxQueuedRequests++
		}
		if NewCapacityInput(settings, capacityTestWindows, option, &frozen).Key() == baseline {
			t.Fatalf("%s reused old capacity input", change)
		}
	}
}

func TestCapacityInfeasibleRankingPrefersOnTimeOverLate(t *testing.T) {
	p := capacityDeltaTiny([]float64{10}, 1)
	late := capacityResult{joules: 1, feedback: []FeedbackWorkloadScore{{UID: "w", Population: 10, Late: 10}}}
	reject := capacityResult{joules: 100, feedback: []FeedbackWorkloadScore{{UID: "w", Population: 10, OnTime: 7, Rejected: 3, FailureFraction: .3}}}
	p.rankFeedback(&late)
	p.rankFeedback(&reject)
	if !reject.better(&late) || late.better(&reject) {
		t.Fatalf("late completions outranked on-time success: late=%+v reject=%+v", late, reject)
	}
}

func TestReplayStoredContractKeepsSixDisjointOutcomes(t *testing.T) {
	_, snapshot := schedulerFixture()
	snapshot.Instances[0].ArrivalAgesMS = map[string][]float64{"w1": {3, 7}}
	counts := map[string]int{schedulerOutcomeOnTime: 2, schedulerOutcomeLate: 3, schedulerOutcomeRejected: 4, schedulerOutcomeTimeout: 5, schedulerOutcomeFailed: 6, schedulerOutcomeUnfinished: 7}
	candidate := SchedulerCandidate{SLO: []SchedulerEstimate{{UID: "w1", PredictedOutcomes: counts}}, Feedback: []FeedbackWorkloadScore{{UID: "w1", Population: 27, OnTime: 2, Late: 3, Rejected: 4, Timeout: 5, Failed: 6, Unfinished: 7}}}
	record := struct {
		Snapshot  SchedulerSnapshot
		Candidate SchedulerCandidate
	}{snapshot, candidate}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{"deadlineDropped", "lifetimeExpired", "\"completed\""} {
		if strings.Contains(string(data), obsolete) {
			t.Fatalf("obsolete outcome remains in stored contract: %s", obsolete)
		}
	}
	var restored struct {
		Snapshot  SchedulerSnapshot
		Candidate SchedulerCandidate
	}
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Candidate.SLO[0].PredictedOutcomes, counts) || !reflect.DeepEqual(restored.Candidate.Feedback, candidate.Feedback) || !reflect.DeepEqual(restored.Snapshot.Instances[0].ArrivalAgesMS, snapshot.Instances[0].ArrivalAgesMS) {
		t.Fatal("stored snapshot/estimate/feedback lost admission evidence or outcomes")
	}
}

func TestReplayHydratesNativeArrivalHistoryAtSnapshotAge(t *testing.T) {
	s := replayAdmissionState(t)
	s.snap.Instances[0].ArrivalAgesMS = map[string][]float64{"w1": {3, 7}}
	s.snap.At = s.snap.At.Add(2 * time.Millisecond)
	s.initializeInstances()
	if history := s.instances[0].arrivalTimes["w1"]; len(history) != 2 || history[0] != -5e6 || history[1] != -9e6 {
		t.Fatalf("captured arrival ages must advance by the 2 ms snapshot age: %v", history)
	}
}

func TestReplayQueueLimitDispatchesBetweenSimultaneousArrivals(t *testing.T) {
	for _, policy := range []AdmissionPolicy{AdmissionPolicyReject, AdmissionPolicyDelay} {
		t.Run(string(policy), func(t *testing.T) {
			settings, snapshot, candidate := feedbackFixture()
			snapshot.Workloads[0].Admission = Admission{Policy: policy, MaxQueuedRequests: 1}
			result := feedbackEvaluateForTest(t, settings, snapshot, candidate)
			score := result.Workloads[0]
			if score.Population != 2 || score.OnTime != 2 || score.Rejected != 0 {
				t.Fatalf("free executor must drain each Accept before the next queue-limit check: %+v", result)
			}
		})
	}
}

func TestReplayPreparedActorStartsWithEmptyArrivalHistory(t *testing.T) {
	s := replayAdmissionState(t)
	instance := s.instances[0]
	instance.arrivalTimes["stale"] = []float64{-1, -2}
	s.actions = []*schedulerReplayAction{{action: Action{Kind: schedulerActionPrepare, Instance: instance.config.ID}, started: true}}
	if !s.completeActions() || len(instance.arrivalTimes) != 0 {
		t.Fatal("new physical actor inherited stale arrival evidence")
	}
	request := replayAdmissionPending(s, "after-prepare", s.snap.Workloads[0])
	if !s.routeRequests(context.Background()) || !request.dispatched {
		t.Fatalf("prepared capacity took no request: %+v", request)
	}
}
