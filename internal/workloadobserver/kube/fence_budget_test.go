package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/workloadobserver"
	"k8s.io/apimachinery/pkg/watch"
)

func TestCausalFenceRetainsOriginalAttemptCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "runtime failure during drain"
		if deadline {
			name = "global deadline during drain"
		}
		t.Run(name, func(t *testing.T) {
			observer := mustObserver(t, sourceWithRunPod(watch.NewRaceFreeFake()))
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			if deadline {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			}
			request := testRequest(4)
			request.EndFenceTimeout = 181 * time.Second
			started, end := make(chan struct{}), make(chan struct{})
			done := make(chan observationResult, 1)
			go func() {
				result, err := observer.Observe(ctx, end, request, func() { close(started) })
				done <- observationResult{value: result, err: err}
			}()
			waitClosed(t, started)
			close(end)
			if !deadline {
				time.Sleep(10 * time.Millisecond)
				cancel(errors.New("runtime failed"))
			}
			select {
			case result := <-done:
				if result.err != nil || result.value.Continuous || result.value.Failure == nil ||
					result.value.Failure.Reason != workloadobserver.FailureReasonEndFenceFailed {
					t.Fatalf("aborted attempt retained successful coverage: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("causal drain detached original attempt cancellation/deadline")
			}
		})
	}
}
