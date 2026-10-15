package serving

import (
	"context"
	"errors"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
)

func TestReturningTailStartsAtExchangeNotEarlySDKCompletion(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.Controls.Enabled = false
	snapshot.Instances[0] = schedulerMeasuredInstance(snapshot.Instances[0], snapshot.Workloads[0], 100*time.Millisecond)
	d := snapshot.Workloads[0]
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close(context.Background()) }()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, Journal: j})
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
		pending := r.SchedulerOutstanding(time.Now())
		if len(pending) != 1 || pending[0].ExchangeResidual != nil || pending[0].Phase == phaseReturning || !call.backendDone || call.progress == nil || call.progress.Phase != "finished" || !call.progress.ExecutionComplete {
			t.Fatalf("early SDK completion must preserve native confirmation without claiming exchange completion: %+v", pending)
		}
		// The SDK callback can precede HTTP completion by a material return leg.
		time.Sleep(20 * time.Millisecond)
		return BackendResult{Finished: true, Progress: &progress, ClientSpan: time.Since(started)}, nil
	}, Dispatch{})
	snapshot.At = call.exchangeFinishedAt.Add(2 * time.Millisecond)
	snapshot.Nodes[0].ObservedAt = snapshot.At
	snapshot.Instances[0].ObservedAt = snapshot.At
	snapshot.Outstanding = r.SchedulerOutstanding(snapshot.At)
	decision := EvaluateScheduler(settings, snapshot)
	estimate := schedulerRequireEstimate(t, decision.Keep, d.UID)
	if estimate.ExpectedNanos == nil || *estimate.ExpectedNanos != float64(106*time.Millisecond) {
		t.Fatalf("SDK-to-exchange leg must not be subtracted twice from the response tail: %+v", estimate)
	}
	if call.receipt.Deadline != deadline || snapshot.Outstanding[0].RemainingLifetime == nil || *snapshot.Outstanding[0].RemainingLifetime != deadline.Sub(snapshot.At) {
		t.Fatal("exchange accounting changed the original accepted deadline")
	}
}

func TestSchedulerRetirementPreservesAcceptedIngressAndPhysicalCancellation(t *testing.T) {
	_, snapshot := schedulerFixture()
	d := snapshot.Workloads[0]
	p := snapshot.Instances[0].Profile
	tensors := []modelcompat.InputTensor{{Name: "input", Datatype: "FP32", SampleShape: []int32{1}}}
	d.Model.Paths[0].Path = p.Condition.Artifact.Path
	d.Model.Paths[0].Tensors = tensors
	p.Condition.LogicalModel = d.Model.Name
	p.Condition.Tensor.Tensors = tensors
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, Journal: j})
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
	instance := ReadyInstance{State: InstanceState{ID: "i1", Node: snapshot.Nodes[0].Node, Pool: "pool", Profile: p.ID, Ready: true, ObservedAt: actualAt, Native: &native.State{}}, Profile: p, Weight: 1, Capacity: 1, Execute: func(ctx context.Context, dispatch Dispatch) (BackendResult, error) {
		started <- dispatch
		<-ctx.Done()
		exchanged <- struct{}{}
		return BackendResult{}, ctx.Err()
	}}
	if err = r.InstallInstances([]ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	r.EnableSchedulerRouting()
	input := &InferenceInput{Inputs: []TensorInput{{Name: "input", Datatype: "FP32", Shape: []int32{1}, Data: []byte("[1]")}}}
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
	if err = r.ApplySchedulerTarget(SchedulerCandidate{}); err != nil {
		t.Fatal(err)
	}
	if stopped, err := r.StopSchedulerInstance("i1"); err != nil || stopped {
		t.Fatalf("accepted ingress stranded: %v %v", stopped, err)
	}
	if state := r.Pools()[0].Instances[0]; !state.Ready || !state.ObservedAt.Equal(actualAt) {
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
	d.Model.Paths[0].Path = p.Condition.Artifact.Path
	p.Condition.LogicalModel = d.Model.Name
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	r, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 1, Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	defer r.cancel()
	r.SetProfiles([]Profile{*p})
	if err = r.Publish(d); err != nil {
		t.Fatal(err)
	}
	r.EnableSchedulerRouting()
	if err = r.InstallInstances([]ReadyInstance{{State: InstanceState{ID: "new", Node: snapshot.Nodes[0].Node, Pool: "pool", Profile: p.ID, Ready: true}, Profile: p, Weight: 1, Capacity: 1, Execute: func(context.Context, Dispatch) (BackendResult, error) { return BackendResult{Finished: true}, nil }}}); err != nil {
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
