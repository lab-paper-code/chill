package serving

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
)

// routingRuntime installs one instance compatible with a controlled and a disabled
// Workload; seed is the instance's routing as read from its Pod.
func routingRuntime(t *testing.T, seed map[string]int32) (*Runtime, SchedulerInstance, map[string]map[string]int32) {
	t.Helper()
	_, snapshot := schedulerFixture()
	controlled := snapshot.Workloads[0]
	disabled := *controlled
	disabled.Name, disabled.UID = "disabled", "w2"
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.cancel)
	i := snapshot.Instances[0]
	r.SetProfiles([]Profile{*i.Profile})
	for _, d := range []*Definition{controlled, &disabled} {
		if err = r.Publish(d); err != nil {
			t.Fatal(err)
		}
	}
	if err = r.InstallInstances([]ReadyInstance{{State: InstanceState{ID: i.ID, Node: i.Node, Profile: i.Profile.ID, Ready: true}, Profile: i.Profile, Weight: 1, Routing: seed, Capacity: 1,
		Execute: func(context.Context, Dispatch) (BackendResult, error) { return BackendResult{Finished: true}, nil }}}); err != nil {
		t.Fatal(err)
	}
	return r, i, map[string]map[string]int32{controlled.UID: {i.ID: 1}, disabled.UID: {}}
}

// E1: after a ChillServer restart the instance bound only to the tight-SLO
// Workload served every compatible Workload, including disabled ones.
func TestRuntimeRestartKeepsPersistedRouting(t *testing.T) {
	r, _, want := routingRuntime(t, map[string]int32{"w1": 1})
	for _, phase := range []string{"restored", "autonomy enabled"} {
		if phase == "autonomy enabled" {
			r.EnableSchedulerRouting()
		}
		if weights, _ := r.SchedulerRouting(); !reflect.DeepEqual(weights, want) {
			t.Fatalf("%s: %v, want %v", phase, weights, want)
		}
	}
}

// Disabling autonomy keeps the applied distribution, not the seed observed before it.
func TestDisableSchedulerRoutingKeepsAppliedDistribution(t *testing.T) {
	r, i, want := routingRuntime(t, nil)
	r.EnableSchedulerRouting()
	i.WorkloadUIDs = []string{"w1"}
	if err := r.ApplySchedulerTarget(SchedulerCandidate{ID: "applied", Instances: []SchedulerInstance{i}, Routing: want}); err != nil {
		t.Fatal(err)
	}
	r.DisableSchedulerRouting()
	if weights, _ := r.SchedulerRouting(); !reflect.DeepEqual(weights, want) {
		t.Fatalf("disabled autonomy rebuilt %v, want %v", weights, want)
	}
}

func TestSchedulerOutstandingUsesCurrentReadTimeAfterCaptureIO(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	settings.MaxObservationAge = 5 * time.Second
	d := snapshot.Workloads[0]
	d.SLO.TargetLatency = time.Second
	cutoff := snapshot.At
	readAt := cutoff.Add(3 * time.Second)
	clock := func() time.Time { return readAt }
	receivedAt := cutoff.Add(-500 * time.Millisecond)
	call := &Call{
		receipt: Receipt{
			ID: "capture-io", UID: d.UID, ReceivedAt: receivedAt, Deadline: receivedAt.Add(5 * time.Second),
		},
		definition: d, identity: d.Identity(),
	}
	runtime := &Runtime{calls: map[string]*Call{call.receipt.ID: call}}
	current := runtime.SchedulerInputs(clock)
	snapshot.At, snapshot.Outstanding = current.At, current.Outstanding
	request := snapshot.Outstanding[0]
	wantElapsed := clock().Sub(receivedAt)
	wantRemaining := call.receipt.Deadline.Sub(clock())
	if request.Elapsed != wantElapsed || request.RemainingLifetime == nil ||
		*request.RemainingLifetime != wantRemaining {
		t.Errorf("capture I/O was omitted from request age/lifetime: elapsed=%s remaining=%v; want %s/%s",
			request.Elapsed, request.RemainingLifetime, wantElapsed, wantRemaining)
	}
	decision := schedulerEvaluateForTest(t, settings, snapshot)
	estimate := schedulerRequireEstimate(t, decision.Keep, d.UID)
	wantCompletion := float64(wantElapsed) + snapshot.Instances[0].Profile.Batches[0].DurationNanos[0]
	if estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != wantCompletion ||
		estimate.NormalizedExceedance <= 0 {
		t.Fatalf("decision concealed already elapsed SLO time: %+v; want completion=%g", estimate, wantCompletion)
	}
}

func TestReplayArrivalHistoryAgesFromNativeObservation(t *testing.T) {
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	snapshot.At = snapshot.At.Add(3 * time.Second)
	snapshot.Instances[0].ArrivalAges = map[string][]time.Duration{
		d.UID: {10 * time.Millisecond, 20 * time.Millisecond},
	}
	arrival := &schedulerReplayRequest{
		source: SchedulerRequest{
			ID: "after-capture", Definition: d, Instance: "i1", ReceivedAt: snapshot.At,
		},
		identity:   d.Identity(),
		dispatched: true,
	}
	state := schedulerReplayState{
		snap: snapshot, c: SchedulerCandidate{Keep: true, Instances: snapshot.Instances},
		requests: []*schedulerReplayRequest{arrival},
	}
	state.initializeInstances()
	state.receiveNative()
	rate := state.nativeRequestState(state.byID["i1"], arrival).ArrivalRate
	wantRate := float64(time.Second) / float64(3*time.Second+10*time.Millisecond)
	if rate != wantRate {
		t.Fatalf("policy arrival rate omitted native observation age: got %g, want %g", rate, wantRate)
	}
}

func TestSchedulerInputsFreezeDispatchedOwnershipAndWeightedCredits(t *testing.T) {
	_, snapshot := schedulerFixture()
	snapshot.At = time.Now()
	snapshot.ObservationEnd = snapshot.At
	snapshot.Nodes[0].ObservedAt = snapshot.At
	snapshot.Instances[0].ObservedAt = snapshot.At
	d := snapshot.Workloads[0]
	second := schedulerTestCPU(snapshot.Instances[0])
	second.ID, second.Weight = "i2", 2
	snapshot.Instances = append(snapshot.Instances, second)
	observationLog, err := NewObservationLog(ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence",
		Writer: &observationArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationLog.Close(context.Background()) }()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	r.SetProfiles([]Profile{*snapshot.Instances[0].Profile, *second.Profile})
	if err = r.Publish(d); err != nil {
		t.Fatal(err)
	}
	instances := make([]ReadyInstance, 0, len(snapshot.Instances))
	dispatched := make(chan Dispatch, 2)
	for _, wanted := range snapshot.Instances {
		instances = append(instances, ReadyInstance{
			State: InstanceState{
				ID: wanted.ID, Node: wanted.Node, Profile: wanted.Profile.ID, Ready: true,
			},
			Profile: wanted.Profile, Weight: wanted.Weight, Capacity: wanted.Capacity,
			Execute: func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
				started := time.Now()
				dispatched <- dispatch
				progress := &BackendProgress{
					RequestID: dispatch.RequestID, WorkloadUID: dispatch.Definition.UID,
					Definition: dispatch.Definition.Identity(), Phase: phaseFinished, ExecutionComplete: true,
				}
				return BackendResult{Finished: true, Progress: progress, ClientSpan: time.Since(started)}, nil
			},
		})
	}
	if err = r.InstallInstances(instances); err != nil {
		t.Fatal(err)
	}
	target := SchedulerCandidate{
		Instances: snapshot.Instances, Routing: map[string]map[string]int32{d.UID: {"i1": 1, "i2": 2}},
	}
	if err = r.ApplySchedulerTarget(target); err != nil {
		t.Fatal(err)
	}
	early := r.SchedulerInputs(time.Now)
	// Publication and assignment happen between earlier observations and the final read.
	late := *d
	late.Name, late.UID = "after-io-workload", "after-io-uid"
	if err = r.Publish(&late); err != nil {
		t.Fatal(err)
	}
	input := &InferenceInput{
		Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}},
	}
	var current SchedulerSnapshot
	actualNext := ""
	for turn := range 2 {
		receipt, receiveErr := r.Receive(d.Name, time.Now())
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		call, acceptErr := r.Accept(receipt, input)
		if acceptErr != nil {
			t.Fatal(acceptErr)
		}
		if _, waitErr := call.Wait(context.Background()); waitErr != nil {
			t.Fatal(waitErr)
		}
		dispatch := <-dispatched
		if dispatch.RequestID != receipt.ID {
			t.Fatal("real dispatch did not belong to the accepted request")
		}
		if turn == 0 {
			current = r.SchedulerInputs(time.Now)
		} else {
			actualNext = dispatch.Assignment.Instance
		}
		if err = r.Response(receipt, outcomeCompleted, ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(current.Workloads) != len(early.Workloads)+1 || len(current.Outstanding) != 1 ||
		current.Outstanding[0].Phase != phaseReturning {
		t.Fatalf("final read missed publication or retained dispatched ownership: %+v", current)
	}
	snapshot.At, snapshot.Workloads, snapshot.Outstanding = current.At, current.Workloads, current.Outstanding
	snapshot.Routing, snapshot.RoutingCredits = current.Routing, current.RoutingCredits
	state := schedulerReplayState{
		snap: snapshot, c: SchedulerCandidate{Keep: true, Instances: snapshot.Instances},
		routeCredits: map[string][]int64{},
		weights:      make([]int32, len(instances)), available: make([]bool, len(instances)),
		out: schedulerReplayResult{definitions: map[string]*Definition{}, outcomes: map[string]map[string]int{}},
	}
	state.initializeInstances()
	state.initializeOutstanding()
	next := &schedulerReplayRequest{source: SchedulerRequest{ID: "next", Definition: d}, identity: d.Identity()}
	state.requests = append(state.requests, next)
	state.routeRequests()
	if actualNext == "" || !next.dispatched || next.source.Instance != actualNext {
		t.Fatalf("post-capture credits predicted %s, next real dispatch was %s", next.source.Instance, actualNext)
	}
}

func TestProgressSinceReachesRestoredAssignments(t *testing.T) {
	start := time.Now()
	restored := start.Add(-time.Minute)
	r := &Runtime{calls: map[string]*Call{
		"restored": {assignment: &Assignment{Instance: "i1", DispatchedAt: restored}},
		"current":  {assignment: &Assignment{Instance: "i1", DispatchedAt: start.Add(time.Second)}},
		"other":    {assignment: &Assignment{Instance: "i2", DispatchedAt: start.Add(-time.Hour)}},
		"queued":   {},
	}}
	if since := r.ProgressSince("i1", start); !since.Equal(restored) {
		t.Fatalf("restored assignment completion would be skipped: %v", since)
	}
	if since := r.ProgressSince("i3", start); !since.Equal(start) {
		t.Fatalf("idle instance replays earlier process output: %v", since)
	}
}

func TestReturningTailStartsAtExchangeNotEarlySDKCompletion(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	settings.CompletionMarginMS = 100
	d := snapshot.Workloads[0]
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationLog.Close(context.Background()) }()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	received := time.Now().Add(-10 * time.Millisecond)
	deadline := received.Add(time.Second)
	call := &Call{receipt: Receipt{ID: "early-sdk", UID: d.UID, ReceivedAt: received, Deadline: deadline}, definition: d, identity: d.Identity(), assignment: &Assignment{Instance: "i1"}, backendContext: context.Background()}
	r.calls[call.receipt.ID] = call
	progress := BackendProgress{RequestID: call.receipt.ID, WorkloadUID: d.UID, Definition: d.Identity(), Phase: "finished", At: time.Now(), ExecutionComplete: true, NativeElapsedMS: 6}
	r.execute(call, func(context.Context, Dispatch) (BackendResult, error) {
		started := time.Now()
		if err := r.ObserveBackend("i1", progress); err != nil {
			t.Fatal(err)
		}
		pending := r.SchedulerInputs(time.Now).Outstanding
		if len(pending) != 1 || pending[0].Phase == phaseReturning || !call.backendDone || call.progress == nil || call.progress.Phase != "finished" || !call.progress.ExecutionComplete {
			t.Fatalf("early SDK completion must preserve native confirmation without claiming exchange completion: %+v", pending)
		}
		// The SDK callback can precede HTTP completion by a material return leg.
		time.Sleep(20 * time.Millisecond)
		return BackendResult{Finished: true, Progress: &progress, ClientSpan: time.Since(started)}, nil
	}, Dispatch{})
	snapshot.At = call.exchangeFinishedAt.Add(2 * time.Millisecond)
	snapshot.ObservationEnd = snapshot.At
	snapshot.Nodes[0].ObservedAt = snapshot.At
	snapshot.Instances[0].ObservedAt = snapshot.At
	current := r.SchedulerInputs(func() time.Time { return snapshot.At })
	snapshot.At, snapshot.Outstanding = current.At, current.Outstanding
	decision := schedulerEvaluateForTest(t, settings, snapshot)
	estimate := schedulerRequireEstimate(t, decision.Keep, d.UID)
	if want := float64(call.exchangeFinishedAt.Sub(received) + 100*time.Millisecond); estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != want {
		t.Fatalf("completion margin must start at exchange completion, not the early SDK callback: %+v want %v", estimate, want)
	}
	if call.receipt.Deadline != deadline || snapshot.Outstanding[0].RemainingLifetime == nil || *snapshot.Outstanding[0].RemainingLifetime != deadline.Sub(snapshot.At) {
		t.Fatal("exchange accounting changed the original accepted deadline")
	}
}

func TestSchedulerRetirementPreservesAcceptedIngressAndPhysicalCancellation(t *testing.T) {
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	p := snapshot.Instances[0].Profile
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	r.SetProfiles([]Profile{*p})
	if err = r.Publish(d); err != nil {
		t.Fatal(err)
	}
	started := make(chan Dispatch, 2)
	exchanged := make(chan struct{}, 2)
	actualAt := time.Now().Add(-time.Second)
	instance := ReadyInstance{State: InstanceState{ID: "i1", Node: snapshot.Nodes[0].Node, Profile: p.ID, Ready: true, ObservedAt: actualAt, Native: &native.State{}}, Profile: p, Weight: 1, Capacity: 1, Execute: func(ctx context.Context, dispatch Dispatch) (BackendResult, error) {
		started <- dispatch
		<-ctx.Done()
		exchanged <- struct{}{}
		return BackendResult{}, ctx.Err()
	}}
	if err = r.InstallInstances([]ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	r.EnableSchedulerRouting()
	input := &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}
	firstReceipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Accept(firstReceipt, input)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first was not dispatched")
	}
	secondReceipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Accept(secondReceipt, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.ApplySchedulerTarget(SchedulerCandidate{Routing: map[string]map[string]int32{d.UID: {}}}); err != nil {
		t.Fatal(err)
	}
	if stopped, err := r.StopSchedulerInstance("i1"); err != nil || stopped {
		t.Fatalf("accepted ingress stranded: %v %v", stopped, err)
	}
	if state := r.Instances()[0]; !state.Ready || !state.ObservedAt.Equal(actualAt) {
		t.Fatalf("retirement or source freshness changed: %+v", state)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = first.Wait(canceled); err == nil {
		t.Fatal("canceled request succeeded")
	}
	select {
	case <-exchanged:
	case <-time.After(time.Second):
		t.Fatal("native exchange was not canceled")
	}
	if err = r.Response(firstReceipt, "canceled", "RequestCanceled"); err != nil {
		t.Fatal(err)
	}
	if detached, err := r.DetachInstance("i1"); err != nil || detached {
		t.Fatalf("transport cancellation freed physical execution: %v %v", detached, err)
	}
	select {
	case <-started:
		t.Fatal("second dispatched before actual completion")
	default:
	}
	if err = r.ObserveBackend("i1", BackendProgress{RequestID: firstReceipt.ID, WorkloadUID: d.UID, Definition: d.Identity(), Phase: "finished", At: time.Now(), ExecutionComplete: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case dispatch := <-started:
		if dispatch.RequestID != secondReceipt.ID {
			t.Fatalf("wrong retained ingress: %+v", dispatch)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted ingress did not resume")
	}
	if stopped, err := r.StopSchedulerInstance("i1"); err != nil || !stopped {
		t.Fatalf("draining could not start: %v %v", stopped, err)
	}
	assertSchedulerTerminationReleasesCall(t, r, second, secondReceipt, canceled, exchanged)
}

func assertSchedulerTerminationReleasesCall(t *testing.T, r *Runtime, second *Call, secondReceipt Receipt, canceled context.Context, exchanged <-chan struct{}) {
	t.Helper()
	_, err := second.Wait(canceled)
	var domain *Error
	if !errors.As(err, &domain) {
		t.Fatalf("cancel outcome missing: %v", err)
	}
	select {
	case <-exchanged:
	case <-time.After(time.Second):
		t.Fatal("second exchange did not finish")
	}
	if err = r.Response(secondReceipt, "canceled", "RequestCanceled"); err != nil {
		t.Fatal(err)
	}
	if err = r.ConfirmInstanceStopped("i1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		detached, err := r.DetachInstance("i1")
		if err != nil {
			t.Fatal(err)
		}
		if detached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual termination did not release ownership")
		}
		select {
		case <-r.Changed():
		case <-time.After(time.Millisecond):
		}
	}
}

func TestSchedulerOffRestoresNewlyDiscoveredStaticCapacity(t *testing.T) {
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	p := snapshot.Instances[0].Profile
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 1, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	r.SetProfiles([]Profile{*p})
	if err = r.Publish(d); err != nil {
		t.Fatal(err)
	}
	r.EnableSchedulerRouting()
	if err = r.InstallInstances([]ReadyInstance{{State: InstanceState{ID: "new", Node: snapshot.Nodes[0].Node, Profile: p.ID, Ready: true}, Profile: p, Weight: 1, Capacity: 1, Execute: func(context.Context, Dispatch) (BackendResult, error) { return BackendResult{Finished: true}, nil }}}); err != nil {
		t.Fatal(err)
	}
	if state, err := r.State(d.Name); err != nil || state.Reason != "Preparing" {
		t.Fatalf("preparation autojoined controlled route: %+v %v", state, err)
	}
	r.DisableSchedulerRouting()
	if state, err := r.State(d.Name); err != nil || state.Reason != "Ready" || !state.Admission {
		t.Fatalf("off mode stranded new static capacity: %+v %v", state, err)
	}
}

func TestControlledRoutingKeepsNewWorkloadQueuedAfterInstanceRediscovery(t *testing.T) {
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	p := snapshot.Instances[0].Profile
	observationLog, err := NewObservationLog(ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence",
		Writer: &observationArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationLog.Close(context.Background()) }()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 1, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	r.SetProfiles([]Profile{*p})
	if err = r.Publish(d); err != nil {
		t.Fatal(err)
	}
	instance := ReadyInstance{
		State: InstanceState{
			ID: "excluded", Node: snapshot.Nodes[0].Node, Profile: p.ID, Ready: true,
		},
		Profile: p, Weight: 1, Capacity: 1,
		Execute: func(context.Context, Dispatch) (BackendResult, error) { return BackendResult{Finished: true}, nil },
	}
	if err = r.InstallInstances([]ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	if err = r.ApplySchedulerTarget(SchedulerCandidate{Routing: map[string]map[string]int32{d.UID: {}}}); err != nil {
		t.Fatal(err)
	}
	next := *d
	next.Name, next.UID = "new-workload", "new-uid"
	if err = r.Publish(&next); err != nil {
		t.Fatal(err)
	}
	if err = r.InstallInstances([]ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.Receive(next.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	input := &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}
	call, err := r.Accept(receipt, input)
	if err != nil {
		t.Fatal(err)
	}
	pending := r.SchedulerInputs(time.Now).Outstanding
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = call.Wait(ctx)
	if err = r.Response(receipt, "canceled", "RequestCanceled"); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != receipt.ID ||
		pending[0].Instance != "" || pending[0].Phase != phasePending {
		t.Fatalf("publication or rediscovery reopened excluded controlled ingress: %+v", pending)
	}
}

func TestSchedulerCandidatesApplyReplayedMultiWorkloadDistribution(t *testing.T) {
	settings, snapshot := schedulerFixture()
	second := *snapshot.Workloads[0]
	second.Name, second.UID = "second-workload", "w2"
	snapshot.Workloads = append(snapshot.Workloads, &second)
	first := snapshot.Instances[0]
	first.WorkloadUIDs = []string{snapshot.Workloads[0].UID, second.UID}
	first.Weight = 3
	last := schedulerTestCPU(first)
	last.ID, last.Weight = "i2", 1
	snapshot.Instances = []SchedulerInstance{first, last}
	snapshot.Routing = map[string]map[string]int32{"w1": {"i2": 1}, "w2": {"i1": 3, "i2": 1}}
	settings.AutonomousWorkloads = nil
	controlled := map[string]bool{"w1": true, "w2": true}
	keep, _ := schedulerEnumerateForTest(t, settings, snapshot)
	t.Run("keep", func(t *testing.T) {
		assertSchedulerCandidateDistribution(t, snapshot, keep[0], [2]int{0, 6}, "")
	})
	for _, routing := range []bool{false, true} {
		settings.Controls.Routing = routing
		e := schedulerEnumeration{ctx: context.Background(), settings: settings, snapshot: snapshot, workloads: snapshot.Workloads, controlled: controlled, common: snapshot.Nodes, seen: map[string]bool{}}
		e.emit(snapshot.Instances)
		expected := [][2]int{{0, 6}}
		name := "routing-disabled"
		if routing {
			name = "routing-enabled"
			expected = [][2]int{{6, 6}, {8, 6}, {0, 6}, {6, 8}, {6, 0}}
		}
		if len(e.result) != len(expected) {
			t.Fatalf("missing distribution alternatives: got %d, want %d", len(e.result), len(expected))
		}
		for n, candidate := range e.result {
			t.Run(name+"/"+candidate.ID, func(t *testing.T) {
				assertSchedulerCandidateDistribution(t, snapshot, candidate, expected[n], "")
			})
		}
	}
	prepared := append([]SchedulerInstance(nil), snapshot.Instances...)
	for n := range prepared {
		profile := *prepared[n].Profile
		profile.ID += "-prepared"
		prepared[n].Profile = &profile
		prepared[n].ID += "-slot"
	}
	settings.Controls.Routing = false
	e := schedulerEnumeration{ctx: context.Background(), settings: settings, snapshot: snapshot, workloads: snapshot.Workloads, controlled: controlled, common: snapshot.Nodes, seen: map[string]bool{}}
	e.emit(prepared)
	t.Run("prepared-physical-identities", func(t *testing.T) {
		assertSchedulerCandidateDistribution(t, snapshot, e.result[0], [2]int{6, 6}, "")
	})
	sharedSnapshot := snapshot
	sharedSnapshot.Instances = []SchedulerInstance{first, last}
	sharedSnapshot.Instances[0].WorkloadUIDs = []string{"w1"}
	sharedSnapshot.Instances[1].WorkloadUIDs = []string{"w2"}
	sharedSnapshot.Routing = map[string]map[string]int32{"w1": {"i1": 3}, "w2": {"i2": 1}}
	shared := schedulerEnumeration{ctx: context.Background(), settings: settings, snapshot: sharedSnapshot, workloads: snapshot.Workloads, controlled: controlled, common: snapshot.Nodes, seen: map[string]bool{}}
	shared.emit([]SchedulerInstance{first})
	t.Run("new-shared-workload-routing-disabled", func(t *testing.T) {
		assertSchedulerCandidateDistribution(t, sharedSnapshot, shared.result[0], [2]int{8, 8}, "")
	})
	creditSnapshot := snapshot
	creditSnapshot.Routing = map[string]map[string]int32{"w1": {"i2": 1}, "w2": {"i1": 1, "i2": 3}}
	for _, routing := range []bool{false, true} {
		settings.Controls.Routing = routing
		credits := schedulerEnumeration{ctx: context.Background(), settings: settings, snapshot: creditSnapshot, workloads: snapshot.Workloads, controlled: controlled, common: snapshot.Nodes, seen: map[string]bool{}}
		credits.emit(snapshot.Instances)
		expected, name := [2]int{0, 2}, "retained"
		if routing {
			expected, name = [2]int{6, 6}, "reset"
		}
		for _, state := range []string{"lazy", "initialized"} {
			t.Run("nonzero-credit/"+name+"/"+state, func(t *testing.T) {
				assertSchedulerCandidateDistribution(t, creditSnapshot, credits.result[0], expected, state)
			})
		}
	}
}

func assertSchedulerCandidateDistribution(t *testing.T, snapshot SchedulerSnapshot, candidate SchedulerCandidate, firstCounts [2]int, creditState string) {
	t.Helper()
	replay := schedulerReplayState{snap: snapshot, c: candidate, routingApplied: true, routeCredits: map[string][]int64{}}
	replay.initializeInstances()
	replay.weights = make([]int32, len(replay.instances))
	replay.available = make([]bool, len(replay.instances))
	for _, wanted := range candidate.Instances {
		replay.byID[wanted.ID].loaded = true
		replay.byID[wanted.ID].ready = true
	}
	for range 8 {
		for _, d := range snapshot.Workloads {
			replay.requests = append(replay.requests, &schedulerReplayRequest{source: SchedulerRequest{Definition: d}, identity: d.Identity()})
		}
	}

	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 32, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	profiles := make([]Profile, 0, len(candidate.Instances))
	instances := make([]ReadyInstance, 0, len(candidate.Instances))
	for _, wanted := range candidate.Instances {
		profiles = append(profiles, *wanted.Profile)
		instances = append(instances, ReadyInstance{
			State:   InstanceState{ID: "physical-" + wanted.ID, TargetID: wanted.ID, Node: wanted.Node, Profile: wanted.Profile.ID, Ready: true},
			Profile: wanted.Profile, Weight: wanted.Weight, Capacity: wanted.Capacity,
			Execute: func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
				return BackendResult{Body: []byte(dispatch.Assignment.Instance), Finished: true}, nil
			},
		})
	}
	r.SetProfiles(profiles)
	for _, d := range snapshot.Workloads {
		if err = r.Publish(d); err != nil {
			t.Fatal(err)
		}
	}
	r.EnableSchedulerRouting()
	if err = r.InstallInstances(instances); err != nil {
		t.Fatal(err)
	}
	input := &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if creditState != "" {
		schedulerPrimeReplayCredits(t, ctx, r, input, &replay, creditState)
	}
	if !replay.applyRoute() {
		t.Fatalf("candidate cannot be replayed: %v", replay.out.reasons)
	}
	replay.routeRequests()
	if err = r.ApplySchedulerTarget(candidate); err != nil {
		t.Fatal(err)
	}
	incomplete := candidate
	incomplete.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: candidate.Routing[snapshot.Workloads[0].UID]}
	var domain *Error
	if err = r.ApplySchedulerTarget(incomplete); !errors.As(err, &domain) || domain.Code != "InvalidDistribution" {
		t.Fatalf("incomplete distribution accepted: %v", err)
	}
	counts := [2]int{}
	for n, predicted := range replay.requests {
		d := predicted.source.Definition
		actual := schedulerActualRoute(t, ctx, r, d, input)
		if !predicted.dispatched || actual != "physical-"+predicted.source.Instance {
			t.Fatalf("request %d (%s): replay chose %q, actual chose %q", n, d.UID, predicted.source.Instance, actual)
		}
		if actual == instances[0].State.ID {
			counts[n%2]++
		}
	}
	if counts != firstCounts {
		t.Fatalf("actual multi-workload distribution %v, want %v", counts, firstCounts)
	}
}

func schedulerActualRoute(t *testing.T, ctx context.Context, r *Runtime, d *Definition, input *InferenceInput) string {
	t.Helper()
	receipt, err := r.Receive(d.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(receipt, input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := call.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Response(receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
	return string(result.Body)
}

func schedulerPrimeReplayCredits(t *testing.T, ctx context.Context, r *Runtime, input *InferenceInput, replay *schedulerReplayState, creditState string) {
	t.Helper()
	previous := replay.c
	previous.Routing = replay.snap.Routing
	if err := r.ApplySchedulerTarget(previous); err != nil {
		t.Fatal(err)
	}
	for _, d := range replay.snap.Workloads {
		schedulerActualRoute(t, ctx, r, d, input)
	}
	_, physicalCredits := r.SchedulerRouting()
	replay.snap.RoutingCredits = map[string]map[string]int64{}
	for uid, values := range physicalCredits {
		replay.snap.RoutingCredits[uid] = map[string]int64{}
		if creditState == "initialized" {
			replay.routeCredits[uid] = make([]int64, len(replay.instances))
		}
		for j, i := range replay.instances {
			credit := values["physical-"+i.config.ID]
			replay.snap.RoutingCredits[uid][i.config.ID] = credit
			if creditState == "initialized" {
				replay.routeCredits[uid][j] = credit
			}
		}
	}
}
