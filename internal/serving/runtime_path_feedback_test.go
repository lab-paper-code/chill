package serving

import (
	"context"
	"fmt"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestRuntimeFreezesAcceptedPostThroughQueueSettingsAndFailure(t *testing.T) {
	for _, post := range []float64{0, 30} {
		t.Run(fmt.Sprint(post), func(t *testing.T) {
			r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1, "w2": 1})
			r.SetCompletionMargin(7)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			seen := make(chan Dispatch, 4)
			release := make(chan struct{}, 4)
			r.instances[instance.ID].Execute = func(ctx context.Context, dispatch Dispatch) (BackendResult, error) {
				seen <- dispatch
				select {
				case <-release:
				case <-ctx.Done():
				}
				return BackendResult{Finished: true}, failure("PreprocessFailed", "test native failure")
			}
			var calls []*Call
			var receipts []Receipt
			accept := func(uid string) {
				t.Helper()
				receipt, err := r.Receive(r.workloads[uid].definition.Name, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
				if err != nil {
					t.Fatal(err)
				}
				calls, receipts = append(calls, call), append(receipts, receipt)
			}
			accept("w1")
			select {
			case dispatch := <-seen:
				if dispatch.CompletionMarginMS != 7 {
					t.Fatalf("initial native reserve changed: %+v", dispatch)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			accept("w1") // Accepted before the update, still waiting for capacity.
			feedback := []PathFeedback{{WorkloadUID: "w1", PostNanos: post * float64(time.Millisecond), Source: "empirical"}}
			r.SetPathFeedback(feedback)
			feedback[0].PostNanos = float64(time.Hour)
			r.SetCompletionMargin(19)
			accept("w1")
			accept("w2") // No published feedback: use the new configured fallback.
			want := map[string]float64{receipts[0].ID: 7, receipts[1].ID: 7, receipts[2].ID: post, receipts[3].ID: 19}
			for _, request := range r.SchedulerInputs(time.Now).Outstanding {
				if request.CompletionMarginMS == nil || *request.CompletionMarginMS != want[request.ID] {
					t.Fatalf("outstanding lost accepted reserve: %+v", request)
				}
				*request.CompletionMarginMS = 999 // Returned pointers must not mutate accepted calls.
			}
			for range calls {
				release <- struct{}{}
			}
			for index, call := range calls {
				if _, err := call.Wait(ctx); !hasRuntimeError(err, "PreprocessFailed") {
					t.Fatalf("native error changed: %v", err)
				}
				if err := r.Response(receipts[index], "preprocess_failed", "PreprocessFailed"); err != nil {
					t.Fatal(err)
				}
			}
			for range calls[1:] {
				select {
				case dispatch := <-seen:
					if dispatch.CompletionMarginMS != want[dispatch.RequestID] {
						t.Fatalf("native reserve changed after acceptance: %+v", dispatch)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			log := r.config.ObservationLog.(*runtimeTestLog)
			if err := log.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			finals := 0
			for _, row := range readRequestRows(t, log.config.Directory) {
				if row.Kind != RequestRowFinal {
					continue
				}
				finals++
				if row.CompletionMarginMS == nil || *row.CompletionMarginMS != want[row.RequestID] {
					t.Fatalf("failed final row lost accepted reserve: %+v", row)
				}
			}
			if finals != len(calls) {
				t.Fatalf("got %d final rows, want %d", finals, len(calls))
			}
		})
	}
}

func TestRuntimeSchedulerInputsUsesNativeElapsedAndStopsAgingTerminalProgress(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.SetCompletionMargin(19)
	r.SetPathFeedback([]PathFeedback{{WorkloadUID: "w1", PostNanos: 0, Source: "empirical"}})
	started := make(chan Dispatch, 1)
	finish, release := context.WithCancel(ctx)
	defer release()
	r.instances[instance.ID].Execute = func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
		started <- dispatch
		<-finish.Done()
		return BackendResult{Finished: true, Progress: &BackendProgress{Phase: "finished", NativeElapsedMS: 12, ExecutionComplete: true}}, nil
	}
	receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	var dispatch Dispatch
	select {
	case dispatch = <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	r.SetPathFeedback([]PathFeedback{{WorkloadUID: "w1", PostNanos: float64(30 * time.Millisecond), Source: "empirical"}})
	r.mu.Lock()
	ready := r.instances[instance.ID].ReadyInstance
	r.mu.Unlock()
	observed := time.Now()
	sender := float64(dispatch.Elapsed) / float64(time.Millisecond)
	ready.State.ObservedAt = observed
	ready.State.Native = &native.State{Requests: []native.RequestState{{
		Context: native.RequestContext{RequestID: receipt.ID, WorkloadUID: receipt.UID, Definition: dispatch.Definition.Identity(), SenderElapsedMS: sender},
		Phase:   "queued", ElapsedMS: 0,
	}}}
	if err := r.InstallInstances([]ReadyInstance{ready}); err != nil {
		t.Fatal(err)
	}
	snapshot := r.SchedulerInputs(func() time.Time { return observed.Add(10 * time.Millisecond) })
	request := snapshot.Outstanding[0]
	if request.NativeElapsedNanos == nil || *request.NativeElapsedNanos != float64(10*time.Millisecond) ||
		request.NativeSenderElapsedMS == nil || *request.NativeSenderElapsedMS != sender ||
		request.CompletionMarginMS == nil || *request.CompletionMarginMS != 0 {
		t.Fatalf("captured native elapsed, sender or accepted zero lost: %+v", request)
	}
	*request.NativeSenderElapsedMS = 999
	release()
	if _, err := call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	terminal := r.SchedulerInputs(func() time.Time { return observed.Add(time.Second) }).Outstanding[0]
	if terminal.NativeElapsedNanos == nil || *terminal.NativeElapsedNanos != float64(12*time.Millisecond) ||
		terminal.NativeSenderElapsedMS == nil || *terminal.NativeSenderElapsedMS != sender ||
		terminal.CompletionMarginMS == nil || *terminal.CompletionMarginMS != 0 {
		t.Fatalf("terminal native evidence aged after completion: %+v", terminal)
	}
	if err := r.Response(receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
}
