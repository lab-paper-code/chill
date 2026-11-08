package serving

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestCapacityPathBudgetsStayWithinWorkloadAndDefinition(t *testing.T) {
	s, snap := capacityFixture()
	s.CompletionMarginMS = 3
	w := snap.Workloads[0]
	other := *w
	other.UID = "other"
	stale := *w
	stale.Generation++
	snap.PathBudgets = []PathBudget{
		{WorkloadUID: other.UID, DefinitionID: other.Identity(), Source: "observed", TotalMS: 30, PostMS: 7, JudgmentPercentile: 97.5},
		{WorkloadUID: w.UID, DefinitionID: stale.Identity(), Source: "observed", TotalMS: 100, PostMS: 100, JudgmentPercentile: 97.5},
		{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: "observed", TotalMS: 21, PostMS: 2, JudgmentPercentile: 97.5},
	}
	row := capacityBuildForTest(t, s, snap)
	if row.Reason != CapacityServiceExceedsSLO || row.Kappa == nil || *row.Kappa != 0 {
		t.Fatalf("selected TOTAL did not reach analytical capacity guard: %+v", row)
	}
	selected := ResolvePathBudget(s, snap.PathBudgets, &other)
	if selected.TotalMS != 30 || selected.PostMS != 7 {
		t.Fatalf("another Workload's budget leaked: %+v", selected)
	}
	missing := *w
	missing.Generation += 2
	fallback := ResolvePathBudget(s, snap.PathBudgets, &missing)
	if fallback.Source != "configured" || fallback.TotalMS != 3 || fallback.PostMS != 3 || fallback.JudgmentPercentile != 97.5 {
		t.Fatalf("missing definition did not use configured split policy: %+v", fallback)
	}
	input := NewCapacityInput(s, snap.DispatchOccupancy, snap.Options[0], w, ResolvePathBudget(s, snap.PathBudgets, w))
	original := input.Key()
	input.PostMarginMS++
	if input.Key() == original {
		t.Fatal("changed native POST reused numerical row")
	}
}

func TestReplayJudgesSplitTailWithoutChangingNativePercentile(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads, snap.Arrivals = nil, nil
	w := snap.Workloads[0]
	w.SLO = SLO{Percentile: 95, TargetLatency: 5 * time.Millisecond}
	for n := range 40 {
		margin := 1.0
		if n >= 38 {
			margin = 10
		}
		snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: fmt.Sprint(n), Definition: w,
			ReceivedAt: snap.At, Phase: phaseReturning, Instance: snap.Instances[0].ID, CompletionMarginMS: &margin})
	}
	result := schedulerKeepForTest(t, s, snap)
	estimate := schedulerRequireEstimate(t, result, w.UID)
	if estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != 10e6 || result.Status != SchedulerStatusOverload || estimate.Percentile != 97.5 {
		t.Fatalf("final quantile used native p95 instead of judging p97.5: %+v %+v", result, estimate)
	}
	r := &schedulerReplayRequest{source: SchedulerRequest{Definition: w}, identity: w.Identity(), postMS: 2, senderMS: 1}
	state := (&schedulerReplayState{}).nativeRequestState(&schedulerReplayInstance{}, r)
	durations := make([]float64, 20)
	for n := range durations {
		durations[n] = 1e6
	}
	durations[19] = 10e6
	batch := schedulerTestBatchProfile(BatchProfile{Batch: 1}, durations)
	policy := firstfloor.Policy{DeadlinePolicy: firstfloor.DeadlineDrop,
		Batches: []firstfloor.BatchProfile{{Size: 1, Distribution: batch.ServiceDistribution}}}
	if choice := firstfloor.ChooseBatch(policy, []firstfloor.RequestState{state}, nil); choice.Size != 1 {
		t.Fatalf("native batch policy used judging tail: %+v %+v", state, choice)
	}
	state.Context.SLO.Percentile = 97.5
	if choice := firstfloor.ChooseBatch(policy, []firstfloor.RequestState{state}, nil); choice.Reason != firstfloor.OutcomeDeadlineDrop {
		t.Fatalf("fixture does not distinguish native and judging tails: %+v", choice)
	}
	limit := capacityWindowLimit{requests: 40, slo: w.SLO, percentile: 97.5}
	late := &schedulerReplayRequest{}
	replay := schedulerReplayState{now: 6e6, capacity: &limit, live: []*schedulerReplayRequest{late}}
	if replay.capacityWindowFailed() || limit.missed != 1 {
		t.Fatal("one miss must fit p97.5 of forty")
	}
	replay.live = append(replay.live, &schedulerReplayRequest{})
	if !replay.capacityWindowFailed() {
		t.Fatal("second miss must disprove p97.5, although native p95 allows it")
	}
	builder := capacityBuilder{settings: s, percentile: 97.5}
	if n := builder.requests(1); float64(n)*(1-.975) < float64(s.CapacityTailSamples) {
		t.Fatalf("population did not cover judging tail: %d", n)
	}
}

func TestReplayExternalBudgetChargesTotalOnce(t *testing.T) {
	for _, tc := range []struct{ total, post float64 }{{20, 3}, {0, 0}, {3, 9}} {
		s, snap := schedulerFixture()
		s.AutonomousWorkloads, snap.Arrivals = nil, nil
		w := snap.Workloads[0]
		w.SLO.TargetLatency = time.Second
		snap.Instances[0].Profile.Batches[0] = schedulerTestBatchProfile(snap.Instances[0].Profile.Batches[0], []float64{1e6})
		budget := PathBudget{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: "observed", TotalMS: tc.total, PostMS: tc.post, JudgmentPercentile: 97.5}
		snap.PathBudgets = []PathBudget{budget}
		snap.Arrivals = []SchedulerArrival{{ID: "arrival", At: snap.ObservationEnd.Add(-s.ObservationWindow), Definition: w, Accepted: true}}
		result := schedulerKeepForTest(t, s, snap)
		estimate := schedulerRequireEstimate(t, result, w.UID)
		if estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != 1e6+tc.total*1e6 {
			t.Fatalf("TOTAL charged incorrectly: total=%g post=%g estimate=%+v reasons=%v", tc.total, tc.post, estimate, result.Reasons)
		}
		reserve, _ := replayPathMargins(budget)
		if reserve < 0 || math.Abs(reserve+tc.post-max(tc.total, tc.post)) > 1e-9 {
			t.Fatalf("invalid native reserve allocation: %g %+v", reserve, budget)
		}
	}
}

func TestReplayAcceptedMarginSurvivesNewBudget(t *testing.T) {
	for _, margin := range []float64{0, 7} {
		s, snap := schedulerFixture()
		s.AutonomousWorkloads, snap.Arrivals = nil, nil
		w := snap.Workloads[0]
		snap.PathBudgets = []PathBudget{{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: "observed", TotalMS: 900, PostMS: 800, JudgmentPercentile: 97.5}}
		snap.Outstanding = []SchedulerRequest{{ID: "old", Definition: w, ReceivedAt: snap.At.Add(-100 * time.Millisecond), Elapsed: 100 * time.Millisecond,
			Phase: phaseReturning, Instance: snap.Instances[0].ID, CompletionMarginMS: &margin}}
		e := schedulerRequireEstimate(t, schedulerKeepForTest(t, s, snap), w.UID)
		if e.ExpectedNanos == nil || *e.ExpectedNanos != 100e6+margin*1e6 {
			t.Fatalf("accepted call acquired latest margin or repeated elapsed F: %+v", e)
		}
	}
}

func TestConfiguredReplayRetainsSenderPlusFullMargin(t *testing.T) {
	s, snap := schedulerFixture()
	s.CompletionMarginMS = 29
	w := snap.Workloads[0]
	budget := ResolvePathBudget(s, nil, w)
	reserve, returned := replayPathMargins(budget)
	r := &schedulerReplayRequest{source: SchedulerRequest{Definition: w}, identity: w.Identity(), senderMS: 50 + reserve, postMS: budget.PostMS}
	state := (&schedulerReplayState{}).nativeRequestState(&schedulerReplayInstance{}, r)
	if returned != 29 || state.Context.SenderElapsedMS != 50 || state.Context.CompletionMarginMS != 29 {
		t.Fatalf("configured pre-F double reservation limitation was silently removed: %+v", state.Context)
	}
	batch := schedulerTestBatchProfile(BatchProfile{Batch: 1}, []float64{30e6})
	policy := firstfloor.Policy{DeadlinePolicy: firstfloor.DeadlineDrop,
		Batches: []firstfloor.BatchProfile{{Size: 1, Distribution: batch.ServiceDistribution}}}
	if choice := firstfloor.ChooseBatch(policy, []firstfloor.RequestState{state}, nil); choice.Reason != firstfloor.OutcomeDeadlineDrop {
		t.Fatalf("configured sender plus full margin must retain historical batch restriction: %+v", choice)
	}
	state.Context.CompletionMarginMS = 0
	if choice := firstfloor.ChooseBatch(policy, []firstfloor.RequestState{state}, nil); choice.Size != 1 {
		t.Fatalf("configured limitation fixture cannot execute without repeated margin: %+v", choice)
	}
}

func TestRuntimePathBudgetFreezesAtAcceptanceAndReachesRows(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := r.workloads["w1"].definition
	dispatched := make(chan Dispatch, 2)
	r.mu.Lock()
	r.instances[instance.ID].outstanding = 1 // Keep both acceptances queued.
	r.instances[instance.ID].Execute = func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
		dispatched <- dispatch
		return BackendResult{Finished: true}, nil
	}
	r.mu.Unlock()
	publish := func(ms float64) {
		r.SetPathBudgets([]PathBudget{{WorkloadUID: d.UID, DefinitionID: d.Identity(), TotalMS: ms + 20, PostMS: ms}})
	}
	accept := func() *Call {
		t.Helper()
		receipt, err := r.Receive(d.Name, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
		if err != nil {
			t.Fatal(err)
		}
		return call
	}
	publish(0)
	first := accept()
	publish(19)
	second := accept()
	r.SetCompletionMargin(99)
	publish(31)
	r.mu.Lock()
	outstanding := r.schedulerOutstandingLocked(time.Now())
	r.instances[instance.ID].outstanding = 0
	r.dispatchLocked()
	r.mu.Unlock()
	frozen := map[string]float64{first.receipt.ID: 0, second.receipt.ID: 19}
	for _, request := range outstanding {
		if request.CompletionMarginMS == nil || *request.CompletionMarginMS != frozen[request.ID] {
			t.Fatalf("snapshot lost frozen zero/POST: %+v", request)
		}
		if request.TotalMarginMS == nil || *request.TotalMarginMS != frozen[request.ID]+20 || request.QueueAge >= request.Elapsed {
			t.Fatalf("snapshot lost frozen TOTAL or included pre-acceptance in slot wait: %+v", request)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, call := range []*Call{first, second} {
		if _, err := call.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		event := requestEvent(call, phaseAccepted, "", "", time.Now())
		row := RequestRow{}
		received := requestEvent(&Call{receipt: call.receipt}, phaseReceived, "", "", call.receipt.ReceivedAt)
		row.Fold(&received)
		if row.CompletionMarginMS != nil {
			t.Fatal("receipt before acceptance manufactured a zero budget")
		}
		row.Fold(&event)
		changed := 999.0
		*event.CompletionMarginMS = changed
		row.Fold(&RequestEvent{CompletionMarginMS: &changed})
		if row.CompletionMarginMS == nil || *row.CompletionMarginMS != frozen[call.receipt.ID] {
			t.Fatalf("row changed acceptance budget: %+v", row)
		}
	}
	for range 2 {
		select {
		case dispatch := <-dispatched:
			if dispatch.CompletionMarginMS != frozen[dispatch.RequestID] {
				t.Fatalf("dispatch used latest budget: %+v", dispatch)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestReplayMixedWorkloadsUseTheirOwnBudgets(t *testing.T) {
	s, snap := schedulerFixture()
	s.AutonomousWorkloads = nil
	w := snap.Workloads[0]
	other := *w
	other.UID, other.Name = "other", "other"
	snap.Workloads = append(snap.Workloads, &other)
	batch := schedulerTestBatchProfile(BatchProfile{Batch: 1, MemoryBytes: 100}, []float64{1e6})
	snap.Instances[0].Profile.Batches = []BatchProfile{batch}
	i := snap.Instances[0]
	i.ID, i.WorkloadUID, i.Routable = "other-instance", other.UID, map[string]bool{other.UID: true}
	snap.Instances = append(snap.Instances, i)
	snap.PathBudgets = []PathBudget{
		{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: "observed", TotalMS: 20, PostMS: 3, JudgmentPercentile: 100},
		{WorkloadUID: other.UID, DefinitionID: other.Identity(), Source: "observed", TotalMS: 7, PostMS: 2, JudgmentPercentile: 100},
	}
	for _, d := range snap.Workloads {
		snap.Arrivals = append(snap.Arrivals, SchedulerArrival{ID: d.UID, At: snap.ObservationEnd.Add(-s.ObservationWindow), Definition: d, Accepted: true})
	}
	result := schedulerKeepForTest(t, s, snap)
	for uid, want := range map[string]float64{w.UID: 21e6, other.UID: 8e6} {
		estimate := schedulerRequireEstimate(t, result, uid)
		if estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != want {
			t.Fatalf("mixed Workload budget crossed definitions: uid=%s estimate=%+v reasons=%v", uid, estimate, result.Reasons)
		}
	}
}

func TestReplayRetainedRequestsRespectSenderFreezeBoundary(t *testing.T) {
	for _, tc := range []struct {
		phase               string
		frozen              bool
		elapsed, wait, want time.Duration
	}{
		{phasePending, false, 12 * time.Millisecond, 10 * time.Millisecond, 31 * time.Millisecond},
		{phaseAssigned, false, 12 * time.Millisecond, 10 * time.Millisecond, 31 * time.Millisecond},
		{phaseAssigned, true, 12 * time.Millisecond, 10 * time.Millisecond, 16 * time.Millisecond},
		{phaseQueued, true, 12 * time.Millisecond, 0, 16 * time.Millisecond},
		// Consumed pre exceeds the modeled allocation: do not make it negative
		// or subtract actual pre again from native's frozen POST.
		{phasePending, false, 40 * time.Millisecond, 10 * time.Millisecond, 44 * time.Millisecond},
	} {
		t.Run(fmt.Sprintf("%s/frozen=%t/elapsed=%s", tc.phase, tc.frozen, tc.elapsed), func(t *testing.T) {
			s, snap := schedulerFixture()
			s.AutonomousWorkloads, snap.Arrivals = nil, nil
			w := snap.Workloads[0]
			w.SLO.TargetLatency = time.Second
			snap.Instances[0].Profile.Batches[0] = schedulerTestBatchProfile(snap.Instances[0].Profile.Batches[0], []float64{1e6})
			total, post, sender := 20.0, 3.0, float64(tc.elapsed)/1e6
			request := SchedulerRequest{ID: "retained", Definition: w, ReceivedAt: snap.At.Add(-tc.elapsed), Elapsed: tc.elapsed,
				QueueAge: tc.wait, Phase: tc.phase, TotalMarginMS: &total, CompletionMarginMS: &post}
			if tc.phase != phasePending {
				request.Instance = snap.Instances[0].ID
			}
			if tc.frozen {
				request.NativeSenderElapsedMS = &sender
			}
			snap.Outstanding = []SchedulerRequest{request}
			snap.PathBudgets = []PathBudget{{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: "observed", TotalMS: 900, PostMS: 800, JudgmentPercentile: 100}}
			estimate := schedulerRequireEstimate(t, schedulerKeepForTest(t, s, snap), w.UID)
			if estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != float64(tc.want) {
				t.Fatalf("sender boundary charged wrong remaining path: want=%s estimate=%+v", tc.want, estimate)
			}
		})
	}
}

func TestRuntimeCapturesSenderFreezeBeforeBackendReturns(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	d := r.workloads["w1"].definition
	started, release := make(chan Dispatch, 1), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	r.mu.Lock()
	r.instances[instance.ID].Execute = func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
		started <- dispatch
		<-release
		return BackendResult{Finished: true}, nil
	}
	r.mu.Unlock()
	receipt, err := r.Receive(d.Name, time.Now().Add(-2*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case dispatch := <-started:
		snapshot := r.SchedulerInputs(time.Now)
		if len(snapshot.Outstanding) != 1 {
			t.Fatalf("missing active request: %+v", snapshot.Outstanding)
		}
		request := snapshot.Outstanding[0]
		if request.NativeSenderElapsedMS == nil || *request.NativeSenderElapsedMS != float64(dispatch.Elapsed)/1e6 {
			t.Fatalf("assigned request lost exact frozen sender while Execute remains active: %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("native dispatch did not start")
	}
	close(release)
	released = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRequestPathAndSchedulerWaitIgnoreDispatchWallClockShift(t *testing.T) {
	for _, dispatched := range []time.Duration{0, 20 * time.Millisecond} {
		accepted := min(5*time.Millisecond, dispatched)
		for _, shift := range []time.Duration{-time.Hour, time.Hour} {
			received := time.Now()
			d := &Definition{UID: "workload"}
			call := &Call{receipt: Receipt{ID: "request", ReceivedAt: received}, definition: d,
				identity: d.Identity(), acceptedElapsed: accepted, dispatchedElapsed: dispatched,
				assignment: &Assignment{Instance: "instance", DispatchedAt: received.Add(dispatched + shift).UTC()}}
			call.receipt.ServerTiming.Record(ServerSenderFreeze, 30*time.Millisecond)
			row := RequestRow{ExecutionComplete: true, NativeExecutionComplete: true, NativeOutcome: outcomeCompleted}
			queued := time.Unix(500, 0)
			running, finished := queued.Add(2*time.Millisecond), queued.Add(12*time.Millisecond)
			nativeMS := 15.0
			row.NativeQueuedAt, row.NativeRunningAt, row.NativeFinishedAt = &queued, &running, &finished
			row.NativeElapsedMS, row.QueueMS = &nativeMS, 4
			accept := requestEvent(call, phaseAccepted, "", "", received.Add(accepted))
			row.Fold(&accept)
			assign := requestEvent(call, phaseAssigned, "", "", call.assignment.DispatchedAt)
			row.Fold(&assign)
			response := requestEvent(call, eventResponseFinished, outcomeCompleted, "", received.Add(100*time.Millisecond))
			row.Fold(&response)
			path, reason := RequestPathSample(&row)
			wait := dispatched - accepted
			if reason != "" || path.TotalNanos != int64(87*time.Millisecond-wait) || path.PostNanos != int64(55*time.Millisecond) {
				t.Fatalf("diagnostic dispatch clock changed path: dispatch=%s shift=%s path=%+v reason=%s", dispatched, shift, path, reason)
			}
			runtime := &Runtime{calls: map[string]*Call{"request": call}}
			requests := runtime.schedulerOutstandingLocked(received.Add(100 * time.Millisecond))
			if len(requests) != 1 || requests[0].QueueAge != wait {
				t.Fatalf("scheduler server wait used diagnostic wall clock: %+v", requests)
			}
		}
	}
}

func TestInsufficientPathSummaryKeepsConfiguredCapacityPolicy(t *testing.T) {
	s, snap := capacityFixture()
	s.CompletionMarginMS = 21
	w := snap.Workloads[0]
	from, to := snap.At.Add(-time.Minute), snap.At
	summary := PathBudget{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: "configured", Reason: "insufficient_samples",
		From: from, To: to, Samples: 18, Received: 20, Completed: 18, Incomplete: 2, JudgmentPercentile: w.SLO.Percentile}
	snap.PathBudgets = []PathBudget{summary}
	row := capacityBuildForTest(t, s, snap)
	if row.Reason != CapacityServiceExceedsSLO || row.Kappa == nil || *row.Kappa != 0 {
		t.Fatalf("insufficient observations erased the configured path reserve: %+v", row)
	}
	selected := ResolvePathBudget(s, snap.PathBudgets, w)
	if selected.TotalMS != 21 || selected.PostMS != 21 || selected.JudgmentPercentile != 97.5 || row.Input.SLO.Percentile != 95 {
		t.Fatalf("configured summary changed margin or native/judging percentile split: %+v %+v", selected, row.Input)
	}
	if selected.Reason != summary.Reason || selected.From != from || selected.To != to ||
		selected.Samples != 18 || selected.Received != 20 || selected.Completed != 18 || selected.Incomplete != 2 {
		t.Fatalf("configured numerical resolution discarded observation diagnostics: %+v", selected)
	}
	summary.Source, summary.TotalMS, summary.PostMS, summary.JudgmentPercentile = "observed", 0, 0, 97.5
	selected = ResolvePathBudget(s, []PathBudget{summary}, w)
	if selected.TotalMS != 0 || selected.PostMS != 0 || selected.Source != "observed" {
		t.Fatalf("observed zero was replaced by configured margin: %+v", selected)
	}
}
