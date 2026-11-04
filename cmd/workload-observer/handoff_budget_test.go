package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExecuteCausalFenceUsesRemainingAttemptBudget(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		beyondBudget  bool
		cancelAttempt bool
	}{
		{name: "paced producer plus final read"},
		{name: "causal fence beyond frozen attempt fails", beyondBudget: true},
		{name: "original attempt cancellation ends causal drain", cancelAttempt: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory := t.TempDir()
			opts, config := sampleStopConfiguration(t, directory, 5*time.Second, 165, 166)
			payload, err := config.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			opts.runConfig = string(payload)
			// This is the same pacing-aware derivation as the unscaled binary
			// scenarios, not a copied timeout or a nominal-metric watchdog.
			plan := sampleStopExecutionPlan(t, config)
			const producerDuration = 348 * time.Second
			finalReadDelay := 35 * time.Second
			if scenario.beyondBudget {
				finalReadDelay = plan.AttemptTimeout - producerDuration + 35*time.Second
			}
			// Explicit 100:1 wall-time scaling of the caller's derived total
			// (including frozen PacingBudget), producer and final-read delays.
			// The unscaled fence cap respects its >=3s admission rule; only
			// the original caller deadline/cancellation may end draining.
			opts.endWaitTimeout = plan.AttemptTimeout / 100
			opts.endFenceTimeout = plan.WorkloadEndFenceTimeout
			opts.maxEvents, opts.maxResultBytes = plan.MaxWorkloadEvents, plan.MaxWorkloadResultBytes
			pod := workloadTestPod()
			client := fake.NewSimpleClientset(pod.DeepCopy())
			retained := watch.NewRaceFreeFake()
			preflightOnly := pod.DeepCopy()
			preflightOnly.Name, preflightOnly.UID = "preflight-only", "preflight-only-uid"
			preflightOnly.OwnerReferences = nil
			installPodObservationReactors(
				t, client, *pod, *preflightOnly, make(chan string, 2), watch.NewRaceFreeFake(), retained)
			ctx, cancel := context.WithTimeout(context.Background(), opts.endWaitTimeout)
			exited := make(chan struct{})
			defer func() {
				cancel()
				<-exited
			}()
			done := make(chan error, 1)
			go func() {
				defer close(exited)
				done <- execute(ctx, opts, client.CoreV1(), time.Millisecond)
			}()
			waitForFile(t, opts.readyPath)
			if err := worker.PublishSignal(opts.windowStartPath); err != nil {
				t.Fatal(err)
			}
			waitForFile(t, opts.windowReadyPath)
			select {
			case <-time.After(producerDuration / 100):
			case err := <-done:
				t.Fatalf("observer ended before runtime: %v", err)
			}
			if err := worker.PublishSignal(opts.endPath); err != nil {
				t.Fatal(err)
			}
			if scenario.cancelAttempt {
				// Let the real end-signal poll enter causal draining, with no
				// metric-exit fence yet available, before canceling its caller.
				select {
				case <-time.After(100 * time.Millisecond):
				case err := <-done:
					t.Fatalf("observer ended before caller canceled the drain: %v", err)
				}
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled original attempt returned %v", err)
				}
				return
			}
			select {
			case <-time.After(finalReadDelay / 100):
				if scenario.beyondBudget {
					select {
					case err := <-done:
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("over-budget causal fence returned %v", err)
						}
					default:
						t.Fatal("causal drain extended the original attempt deadline")
					}
					return
				}
				fence := pod.DeepCopy()
				fence.ResourceVersion = "21"
				fence.Status.ContainerStatuses = []corev1.ContainerStatus{{
					Name:  endFenceContainerName,
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
				}}
				retained.Modify(fence)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				reader, err := worker.NewWorkloadResultFile(worker.WorkloadResultFileConfig{
					Path: opts.resultPath, WaitTimeout: plan.WorkloadResultTimeout,
					PollInterval: time.Millisecond, MaxBytes: opts.maxResultBytes,
				})
				if err != nil {
					t.Fatal(err)
				}
				verified, err := reader.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				result, err := verified.Record()
				if err != nil || !result.Continuous || result.LastResourceVersion != "21" {
					t.Fatalf("causal handoff lost coverage: result=%+v err=%v", result, err)
				}
			case err := <-done:
				if !scenario.beyondBudget || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("observer did not preserve remaining attempt budget: %v", err)
				}
			}
		})
	}
}
