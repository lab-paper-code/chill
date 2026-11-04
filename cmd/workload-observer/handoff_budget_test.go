package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExecuteCausalFenceUsesRemainingAttemptBudget(t *testing.T) {
	for _, delaySeconds := range []int{35, 80} {
		name := "118s runtime plus 35s final read"
		if delaySeconds == 80 {
			name = "causal fence beyond 181s fails"
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			writeWorkloadTestRunConfig(t, directory)
			opts := workloadTestOptions(directory)
			var config profilingrun.RunConfig
			if err := json.Unmarshal([]byte(opts.runConfig), &config); err != nil {
				t.Fatal(err)
			}
			plan := sampleStopExecutionPlan(t, config)
			if plan.AttemptTimeout != 181*time.Second || plan.WorkloadEndFenceTimeout != plan.AttemptTimeout {
				t.Fatalf("causal fence does not inherit frozen attempt budget: %+v", plan)
			}
			// Explicit 100:1 wall-time scaling of the caller's global budget and
			// producer/final-read delays. Keep the derived fence cap unscaled to
			// respect its existing >=3s admission rule; only the original caller
			// deadline may end draining. Real execute/LIST-WATCH/file handoff run.
			const second = 10 * time.Millisecond
			opts.endWaitTimeout = plan.AttemptTimeout / 100
			opts.endFenceTimeout = plan.WorkloadEndFenceTimeout
			pod := workloadTestPod()
			client := fake.NewSimpleClientset(pod.DeepCopy())
			retained := watch.NewRaceFreeFake()
			preflightOnly := pod.DeepCopy()
			preflightOnly.Name, preflightOnly.UID = "preflight-only", "preflight-only-uid"
			preflightOnly.OwnerReferences = nil
			installPodObservationReactors(
				t, client, *pod, *preflightOnly, make(chan string, 2), watch.NewRaceFreeFake(), retained)
			ctx, cancel := context.WithTimeout(context.Background(), opts.endWaitTimeout)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- execute(ctx, opts, client.CoreV1(), time.Millisecond) }()
			waitForFile(t, opts.readyPath)
			if err := worker.PublishSignal(opts.windowStartPath); err != nil {
				t.Fatal(err)
			}
			waitForFile(t, opts.windowReadyPath)
			select {
			case <-time.After(118 * second):
			case err := <-done:
				t.Fatalf("observer ended before runtime: %v", err)
			}
			if err := worker.PublishSignal(opts.endPath); err != nil {
				t.Fatal(err)
			}
			select {
			case <-time.After(time.Duration(delaySeconds) * second):
				if delaySeconds == 80 {
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
				if delaySeconds == 35 || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("observer did not preserve remaining attempt budget: %v", err)
				}
			}
		})
	}
}
