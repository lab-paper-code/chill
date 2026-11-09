package serving

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeConfiguredTickUsesCurrentMarginForNewAccept(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.SetCompletionMargin(7)
	r.SetPathFeedback([]PathFeedback{BuildPathFeedback("w1", nil, r.workloads["w1"].definition.SLO, 7*time.Millisecond)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := make(chan Dispatch, 2)
	release := make(chan struct{}, 2)
	r.instances[instance.ID].Execute = func(ctx context.Context, dispatch Dispatch) (BackendResult, error) {
		seen <- dispatch
		select {
		case <-release:
		case <-ctx.Done():
		}
		return BackendResult{Finished: true}, nil
	}
	accept := func() (Receipt, *Call) {
		t.Helper()
		receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		call, err := r.Accept(&receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
		if err != nil {
			t.Fatal(err)
		}
		return receipt, call
	}
	first, firstCall := accept()
	r.SetCompletionMargin(19)
	second, secondCall := accept() // No second Tick publishes the new fallback.
	want := map[string]float64{first.ID: 7, second.ID: 19}
	for _, request := range r.SchedulerInputs(time.Now).Outstanding {
		if request.CompletionMarginMS == nil || *request.CompletionMarginMS != want[request.ID] {
			t.Fatalf("configured Tick overrode current or accepted margin: %+v", request)
		}
	}
	for range 2 {
		release <- struct{}{}
	}
	for _, current := range []struct {
		receipt Receipt
		call    *Call
	}{{first, firstCall}, {second, secondCall}} {
		if _, err := current.call.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if err := r.Response(current.receipt, outcomeCompleted, ""); err != nil {
			t.Fatal(err)
		}
		select {
		case dispatch := <-seen:
			if dispatch.CompletionMarginMS != want[dispatch.RequestID] {
				t.Fatalf("native input lost accepted margin: %+v", dispatch)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
