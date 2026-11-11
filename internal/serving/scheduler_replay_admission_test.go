package serving

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestReplayAdmissionChoosesFeasibleExecutor(t *testing.T) {
	s := replayAdmissionState(t)
	slow := s.instances[0]
	profile := *slow.config.Profile
	profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{200e6})}
	slow.config.Profile, slow.policy = &profile, schedulerPolicy(SchedulerInstance{Profile: &profile, MaxBatch: 1})
	fast := *slow
	fast.config.ID, fast.config.Profile = "fast", s.snap.Instances[0].Profile
	fast.policy = schedulerPolicy(fast.config)
	s.instances = append(s.instances, &fast)
	s.byID["fast"] = &fast
	s.weights, s.available = make([]int32, 2), make([]bool, 2)
	r := replayAdmissionPending(s, "candidate", s.snap.Workloads[0])
	s.routeRequests(context.Background())
	if !r.dispatched || r.source.Instance != "fast" || r.dropped {
		t.Fatalf("infeasible executor survived shared admission: %+v", r)
	}
}

func TestReplayAdmissionUnknownAndNoCapacityWaitForLifetime(t *testing.T) {
	for _, cause := range []string{"missing profile", "missing support", "missing arrival history", "running beyond support", "no capacity"} {
		t.Run(cause, func(t *testing.T) {
			s := replayAdmissionState(t)
			i := s.instances[0]
			switch cause {
			case "missing profile":
				i.config.Profile = nil
			case "missing support":
				i.policy.Batches = nil
			case "missing arrival history":
				i.arrivalsKnown = false
			case "running beyond support":
				running := &schedulerReplayRequest{source: SchedulerRequest{ID: "running", Definition: s.snap.Workloads[0]}, identity: s.snap.Workloads[0].Identity(), running: true}
				i.batch, i.exchangeKnown, i.batchElapsed = []*schedulerReplayRequest{running}, true, int64(time.Second)
			case "no capacity":
				i.outstanding = i.config.Capacity
			}
			r := replayAdmissionPending(s, "candidate", s.snap.Workloads[0])
			want := AdmissionUnknown
			if cause == "no capacity" {
				want = "NoCapacity"
			}
			if selected, state := s.chooseAdmission(context.Background(), r); selected >= 0 || state != want {
				t.Fatalf("decision must distinguish absent capacity from unknown evidence: %s %d, want %s", state, selected, want)
			}
			s.routeRequests(context.Background())
			if r.dispatched || r.dropped || r.gatewayDeferred {
				t.Fatalf("unknown/capacity became an infeasibility rejection: %+v", r)
			}
			s.now = r.expires
			s.expireRequests()
			s.completeExecution()
			if !r.expired || s.out.outcomes[r.identity][schedulerOutcomeTimeout] != 1 || s.out.outcomes[r.identity][schedulerOutcomeRejected] != 0 {
				t.Fatalf("waiting request did not retain its final deadline: %+v", r)
			}
		})
	}
}

func TestReplayAdmissionRejectDelayAndSharedNormalPriority(t *testing.T) {
	for _, policy := range []AdmissionPolicy{AdmissionPolicyReject, AdmissionPolicyDelay} {
		t.Run(string(policy), func(t *testing.T) {
			s := replayAdmissionState(t)
			delayed := *s.snap.Workloads[0]
			delayed.UID, delayed.Admission.Policy, delayed.SLO.TargetLatency = "a-delayed", policy, time.Millisecond
			normal := *s.snap.Workloads[0]
			normal.UID = "z-normal"
			s.snap.Routing = map[string]map[string]int32{delayed.UID: {"i1": 1}, normal.UID: {"i1": 1}}
			s.instances[0].config.Capacity = 1
			r := replayAdmissionPending(s, "delayed", &delayed)
			n := replayAdmissionPending(s, "normal", &normal)
			s.routeRequests(context.Background())
			if !n.dispatched || r.dispatched || (policy == AdmissionPolicyReject && !r.dropped) || (policy == AdmissionPolicyDelay && !r.gatewayDeferred) {
				t.Fatalf("all Workloads' normal requests must precede delayed dispatch: delayed=%+v normal=%+v", r, n)
			}
		})
	}
}

func TestReplayGatewayDeferredSnapshotRestoresWithoutNativeInstance(t *testing.T) {
	s := replayAdmissionState(t)
	d := s.snap.Workloads[0]
	d.Admission.Policy = AdmissionPolicyDelay
	s.snap.Outstanding = []SchedulerRequest{{ID: "gateway-delay", Definition: d, ReceivedAt: s.snap.At, Phase: phaseDeferred}}
	s.initializeOutstanding()
	s.admitArrivals(context.Background())
	if len(s.out.reasons) != 0 || len(s.live) != 1 || !s.live[0].gatewayDeferred || s.live[0].dispatched || len(s.instances[0].queue) != 0 {
		t.Fatalf("gateway delay restored as native work: %+v", s)
	}
	if reasons := schedulerValidateRequest(s.snap.Outstanding[0], map[string]bool{"i1": true}, nil); len(reasons) != 0 {
		t.Fatalf("gateway deferred snapshot requires no native identity: %v", reasons)
	}
	s.routeRequests(context.Background())
	if !s.live[0].dispatched || s.live[0].deferred {
		t.Fatalf("restored gateway delay lost its policy: %+v", s.live[0])
	}
}

func TestReplayAdmissionQueueLimitAndRejectedDemand(t *testing.T) {
	s := replayAdmissionState(t)
	d := s.snap.Workloads[0]
	d.Admission.MaxQueuedRequests = 2
	s.instances[0].outstanding = s.instances[0].config.Capacity
	for _, id := range []string{"normal", "delay", "new"} {
		r := replayAdmissionPending(s, id, d)
		r.gatewayDeferred = id == "delay"
	}
	s.live, s.arrived = nil, 0
	s.admitArrivals(context.Background())
	if len(s.live) != 2 || s.out.outcomes[d.Identity()][schedulerOutcomeRejected] != 1 {
		t.Fatalf("queue limit must count normal and delayed requests together: %+v", s.out)
	}
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	snapshot.Arrivals = []SchedulerArrival{{ID: "rejected-demand", Definition: snapshot.Workloads[0], At: snapshot.ObservationEnd.Add(-settings.ObservationWindow), Compute: true, Accepted: false, Outcome: schedulerOutcomeRejected}}
	cohort, err := newFeedbackArrivalCohort(context.Background(), settings, snapshot)
	if err != nil || len(cohort.requests) != 1 || snapshot.Arrivals[0].Accepted {
		t.Fatalf("rejected compute demand disappeared or fabricated acceptance: %+v %v", cohort, err)
	}
}

func TestCapacityAdmissionChangesInvalidateRowAndFeedbackKeys(t *testing.T) {
	settings, snapshot := schedulerFixture()
	definition := snapshot.Workloads[0]
	option := snapshot.Options[0]
	baseline := NewCapacityInput(settings, nil, option, definition).Key()
	for _, change := range []string{"policy", "queue limit"} {
		frozen := *definition
		if change == "policy" {
			frozen.Admission.Policy = AdmissionPolicyDelay
		} else {
			frozen.Admission.MaxQueuedRequests++
		}
		if NewCapacityInput(settings, nil, option, &frozen).Key() == baseline {
			t.Fatalf("%s reused old capacity input", change)
		}
		before, err := JudgeFeedbackRates(context.Background(), settings, option, definition, DispatchOccupancy{}, PathFeedback{}, []float64{1})
		if err != nil {
			t.Fatal(err)
		}
		after, err := JudgeFeedbackRates(context.Background(), settings, option, &frozen, DispatchOccupancy{}, PathFeedback{}, []float64{1})
		if err != nil || len(before.Tests) != 1 || len(after.Tests) != 1 || before.Tests[0].InputKey == after.Tests[0].InputKey {
			t.Fatalf("%s reused old feedback experiment: before=%+v after=%+v err=%v", change, before, after, err)
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

// Both arms use the U3 frozen trace, initial native exchange, measured service
// sequence PCG(23,47), zero POST/transport, and one-second fill budget. Initialize
// the replay exchange at the fixture's fixed physical completion without drawing
// a residual sample: drawing would shift every later sample relative to runtime.
func runReplayAdmissionOverload(t *testing.T, fixture admissionOverloadFixture, policy AdmissionPolicy, rate int) admissionOverloadCounts {
	t.Helper()
	s := replayAdmissionState(t)
	s.snap.At = time.Unix(2000000000, 0).UTC()
	s.horizon = float64(30 * time.Second)
	d := s.snap.Workloads[0]
	d.Name, d.UID, d.SLO, d.Admission = "measured", "measured", SLO{Percentile: 95, TargetLatency: time.Second}, Admission{Policy: policy, MaxQueuedRequests: 1024}
	i := s.instances[0]
	i.config.ID, i.config.WorkloadUID, i.config.Capacity, i.config.MaxBatch, i.config.MaxWait = "jetsono", d.UID, 256, fixture.MaxBatchSize, time.Second
	profile := *i.config.Profile
	profile.ID, profile.Batches = fixture.ProfileID, fixture.Batches
	i.config.Profile, i.policy = &profile, schedulerPolicy(SchedulerInstance{Profile: &profile, MaxBatch: fixture.MaxBatchSize, MaxWait: time.Second})
	s.byID = map[string]*schedulerReplayInstance{"jetsono": i}
	i.arrivalTimes[d.UID] = []float64{-500e6, -500e6}
	zero, nativeAge, lifetime := 0.0, 500e6, 1500*time.Millisecond
	exchange := int64(0)
	for j := range 32 {
		phase := phaseQueued
		if j < 16 {
			phase = phaseRunning
		}
		s.snap.Outstanding = append(s.snap.Outstanding, SchedulerRequest{ID: fmt.Sprintf("initial-%02d", j), Definition: d, ReceivedAt: s.snap.At.Add(-500 * time.Millisecond), Elapsed: 500 * time.Millisecond, QueueAge: 500 * time.Millisecond, Phase: phase, Instance: "jetsono", BatchID: "initial", NativeSenderElapsedMS: &zero, NativeElapsedNanos: &nativeAge, CompletionMarginMS: &zero, ExchangeElapsedNanos: &exchange, RemainingLifetime: &lifetime})
	}
	s.initializeOutstanding()
	i.exchangeKnown = true
	for _, batch := range fixture.Batches {
		if batch.Batch == 16 {
			i.finish = float64(batch.ServiceSamples[0].ServiceDurationNanos)
			i.nativeFinish = i.finish
		}
	}
	for j := range rate * 30 {
		at := time.Duration(int64(j) * int64(time.Second) / int64(rate))
		r := &schedulerReplayRequest{source: SchedulerRequest{ID: fmt.Sprintf("offered-%03d", j), Definition: d, ReceivedAt: s.snap.At.Add(at), Phase: phasePending, ArrivalRate: float64(rate)}, identity: d.Identity(), arrival: float64(at), native: math.Inf(1), expires: float64(at + 2*time.Second)}
		s.requests = append(s.requests, r)
	}
	for step := 0; ; step++ {
		if step >= 20000 {
			t.Fatal("replay overload event bound exceeded")
		}
		s.expireRequests()
		s.completeExecution()
		s.admitArrivals(context.Background())
		s.routeRequests(context.Background())
		s.receiveNative()
		if !s.chooseBatches(context.Background()) {
			t.Fatalf("replay overload failed: %+v", s.out)
		}
		next, remaining := s.nextEvent()
		if !remaining {
			break
		}
		if next <= s.now || math.IsInf(next, 1) {
			t.Fatalf("replay overload stalled: now=%g next=%g", s.now, next)
		}
		s.now = next
	}
	counts := admissionOverloadCounts{}
	for _, r := range s.requests {
		if !strings.HasPrefix(r.source.ID, "offered-") {
			continue
		}
		switch {
		case r.dropped:
			counts.Rejected++
		case r.expired:
			counts.Timeout++
		case !r.completed || r.failed:
			t.Fatalf("offered cohort did not finish: %+v", r)
		case r.completion-r.arrival <= float64(time.Second):
			counts.OnTime++
		default:
			counts.Late++
		}
	}
	return counts
}

func TestReplayRuntimeMeasuredAdmissionOutcomeEquivalence(t *testing.T) {
	fixture := measuredAdmissionFixture(t)
	for _, rate := range []int{31, 44} {
		for _, policy := range []AdmissionPolicy{AdmissionPolicyReject, AdmissionPolicyDelay} {
			t.Run(fmt.Sprintf("%drps/%s", rate, policy), func(t *testing.T) {
				runtime := runAdmissionOverload(t, fixture, string(policy), rate)
				runtime.Executed, runtime.ExecutedPastSLO = 0, 0
				replay := runReplayAdmissionOverload(t, fixture, policy, rate)
				if !reflect.DeepEqual(replay, runtime) {
					t.Fatalf("same trace/physical sequence outcome mismatch: runtime=%+v replay=%+v", runtime, replay)
				}
			})
		}
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

func TestReplayAdmissionHydratesNativeArrivalHistoryAtSnapshotAge(t *testing.T) {
	s := replayAdmissionState(t)
	s.snap.Instances[0].ArrivalAgesMS = map[string][]float64{"w1": {3, 7}}
	s.snap.At = s.snap.At.Add(2 * time.Millisecond)
	s.initializeInstances()
	r := replayAdmissionPending(s, "candidate", s.snap.Workloads[0])
	state := s.gatewayRequestState(s.instances[0], r)
	if !s.instances[0].arrivalsKnown || state.ArrivalRate != 200 {
		t.Fatalf("candidate rate must advance 3ms captured age by 2ms snapshot age: %+v", state)
	}
}

func TestReplayAdmissionFixedBatchKeepsStrictBaselineException(t *testing.T) {
	s := replayAdmissionState(t)
	s.snap.Workloads[0].SLO.TargetLatency = time.Nanosecond
	s.instances[0].policy.FixedBatchSize = 1
	s.instances[0].policy.Batches = nil
	s.instances[0].arrivalsKnown = false
	r := replayAdmissionPending(s, "fixed-candidate", s.snap.Workloads[0])
	s.routeRequests(context.Background())
	if !r.dispatched || r.dropped || r.gatewayDeferred {
		t.Fatalf("strict fixed-batch baseline acquired dynamic SLO admission: %+v", r)
	}
}

func TestFeedbackGatewayDeferredNeedsNoNativeElapsedContext(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	snapshot.Arrivals = nil
	definition := snapshot.Workloads[0]
	definition.Admission.Policy = AdmissionPolicyDelay
	lifetime := time.Second
	snapshot.Outstanding = []SchedulerRequest{{ID: "gateway-deferred", Definition: definition, ReceivedAt: snapshot.At.Add(-20 * time.Millisecond), Elapsed: 20 * time.Millisecond, Phase: phaseDeferred, RemainingLifetime: &lifetime}}
	result, err := evaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1, nil)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 1 || result.Workloads[0].Population != 1 || result.Workloads[0].OnTime != 1 {
		t.Fatalf("gateway-deferred request became missing native evidence: %+v %v", result, err)
	}
	if snapshot.Outstanding[0].NativeElapsedNanos != nil || snapshot.Outstanding[0].NativeSenderElapsedMS != nil {
		t.Fatal("feedback fabricated native timing for gateway ingress")
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

type replayCancelContext struct {
	context.Context
	remaining int
}

func (ctx *replayCancelContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestReplayDispatchHonorsCancellationDuringPreparation(t *testing.T) {
	s := replayAdmissionState(t)
	for j := range 100 {
		replayAdmissionPending(s, fmt.Sprintf("pending-%d", j), s.snap.Workloads[0])
	}
	ctx := &replayCancelContext{Context: context.Background(), remaining: 8}
	if s.routeRequests(ctx) || s.admissionPrepared {
		t.Fatal("canceled preparation continued dispatch or retained mutable indexes")
	}
	for _, request := range s.live {
		if request.dispatched || request.dropped {
			t.Fatalf("canceled preparation changed request outcome: %+v", request)
		}
	}
}

func TestReplayPreparedActorStartsWithKnownEmptyArrivalHistory(t *testing.T) {
	s := replayAdmissionState(t)
	instance := s.instances[0]
	instance.arrivalsKnown = false
	instance.arrivalTimes["stale"] = []float64{-1, -2}
	s.actions = []*schedulerReplayAction{{action: Action{Kind: schedulerActionPrepare, Instance: instance.config.ID}, started: true}}
	if !s.completeActions() || !instance.arrivalsKnown || len(instance.arrivalTimes) != 0 {
		t.Fatal("new physical actor inherited absent or stale arrival evidence")
	}
	request := replayAdmissionPending(s, "after-prepare", s.snap.Workloads[0])
	if !s.routeRequests(context.Background()) || !request.dispatched {
		t.Fatalf("prepared capacity remained permanently AdmissionUnknown: %+v", request)
	}
}
