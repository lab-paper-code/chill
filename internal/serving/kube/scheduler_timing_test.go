package kube

import (
	"context"
	"errors"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSchedulerTickCompletionIncludesKeepAndFailedRecord(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := native.DeadlineKeep
		if fail {
			name = "failed-record"
		}
		t.Run(name, func(t *testing.T) {
			controller := schedulerEmptyPoolController(t)
			failure := errors.New("controlled decision record failure")
			var recordElapsed, launchElapsed time.Duration
			controller.config.OnDecision = func(decision serving.SchedulerDecision, _ serving.SchedulerSnapshot) error {
				if decision.Status != "Keep" {
					t.Fatalf("expected Keep callback: %s", decision.Status)
				}
				started := time.Now()
				time.Sleep(10 * time.Millisecond)
				recordElapsed = time.Since(started)
				if fail {
					return failure
				}
				// Hold the shared state lock after callback return so real launch
				// waits synchronously, without introducing a production test hook.
				controller.mu.Lock()
				go func() {
					started := time.Now()
					time.Sleep(10 * time.Millisecond)
					launchElapsed = time.Since(started)
					controller.mu.Unlock()
				}()
				return nil
			}
			at := time.Now().UTC()
			started := time.Now()
			decision, err := controller.Tick(context.Background(), at)
			callElapsed := time.Since(started)
			if errors.Is(err, failure) != fail {
				t.Fatalf("Tick error: %v", err)
			}
			rows, _, _, _, err := controller.observationLog.SchedulerObservationPage(context.Background(), at.Add(-time.Second), time.Now().Add(time.Second), 100, 0, observation.SchedulerObservationSelection{})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Control == nil {
				t.Fatalf("completion not retained independently of OnDecision: %+v", rows)
			}
			event := rows[0].Control
			if event.Scope != "tick" || !event.TickAt.Equal(at) || event.Status != decision.Status || event.Record < recordElapsed || event.Elapsed > callElapsed || event.Elapsed < decision.EvaluationElapsed+recordElapsed {
				t.Fatalf("Tick omitted synchronous callback or identity: %+v decision=%+v", event, decision)
			}
			if fail && (event.Phase != "record" || event.Error == "" || event.Launch != 0) {
				t.Fatalf("failed callback advanced launch or lost failure: %+v", event)
			}
			if !fail && (event.Phase != "launch" || event.Error != "" || event.Launch < launchElapsed/2) {
				t.Fatalf("Keep launch boundary missing: %+v", event)
			}
		})
	}
}

func TestSchedulerTickCompletionRetainsEarlyFailuresAndLockWait(t *testing.T) {
	controller := schedulerEmptyPoolController(t)
	failure := errors.New("controlled capture failure")
	controller.kube.(*kubefake.Clientset).PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, failure
	})
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		at := time.Now().UTC()
		controller.tickMu.Lock()
		entered, done := make(chan struct{}), make(chan error, 1)
		go func() {
			close(entered)
			_, err := controller.Tick(ctx, at)
			done <- err
		}()
		<-entered
		time.Sleep(10 * time.Millisecond)
		controller.tickMu.Unlock()
		err := <-done
		cancel()
		if err == nil || canceled && !errors.Is(err, context.Canceled) || !canceled && !errors.Is(err, failure) {
			t.Fatalf("early error changed: %v", err)
		}
		rows, _, _, _, err := controller.observationLog.SchedulerObservationPage(context.Background(), at, time.Now().Add(time.Second), 100, 0, observation.SchedulerObservationSelection{Summary: true})
		if err != nil || len(rows) != 1 || rows[0].Control == nil {
			t.Fatalf("early completion unavailable: %+v %v", rows, err)
		}
		event := rows[0].Control
		if event.Error == "" || event.Phase != "capture" || event.LockWait <= 0 || event.Elapsed < event.LockWait+event.Capture || event.Evaluate != 0 || event.Record != 0 || event.Launch != 0 {
			t.Fatalf("early return missing from call denominator: %+v", event)
		}
	}
}

// Context.Err is the existing cancellation boundary. Match the nominal-rate
// caller so search finishes first; no clock race or production hook is needed.
type cancelNominalRateContext struct {
	context.Context
	cancel context.CancelFunc
}

func (c cancelNominalRateContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	var callers [32]uintptr
	frames := goruntime.CallersFrames(callers[:goruntime.Callers(2, callers[:])])
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, "(*capacityPlanner).testDecisionRates") {
			c.cancel()
			return c.Context.Err()
		}
		if !more {
			return nil
		}
	}
}

func TestSchedulerTickNominalRateCancellationRetainsBackendTiming(t *testing.T) {
	for _, strategy := range []string{"v1", "v2"} {
		t.Run(strategy, func(t *testing.T) {
			controller := handoffController(t)
			// Existing clock injection supplies complete observation coverage
			// without waiting for a real control window.
			started := time.Now().Add(-time.Minute)
			var current atomic.Bool
			history, err := observation.NewObservationLog(observation.ObservationLogConfig{
				Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
				Writer: managerArchive{t.TempDir()},
				Now: func() time.Time {
					if !current.Load() {
						return started
					}
					return time.Now()
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			current.Store(true)
			t.Cleanup(func() {
				if err := history.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			controller.observationLog = history
			controller.config.Settings.PlanSearchStrategy = strategy
			uid := controller.config.Settings.AutonomousWorkloads[0]
			controller.config.Settings.MinimumDemand = map[string]float64{uid: 1}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			at := time.Now().UTC()
			decision, err := controller.Tick(cancelNominalRateContext{Context: ctx, cancel: cancel}, at)
			if !errors.Is(err, context.Canceled) || decision.Status != "" || decision.Target != nil {
				t.Fatalf("late evaluation failure became publishable: %+v %v", decision, err)
			}
			rows, _, _, _, err := history.SchedulerObservationPage(context.Background(), at, time.Now().Add(time.Second), 100, 0, observation.SchedulerObservationSelection{})
			if err != nil || len(rows) != 1 || rows[0].Control == nil {
				t.Fatalf("late evaluation completion unavailable: %+v %v", rows, err)
			}
			event := rows[0].Control
			timing := event.Evaluation
			if event.Error != context.Canceled.Error() || event.Phase != "evaluate" || event.Status != "" || !event.DecisionAt.IsZero() || event.Record != 0 || event.Launch != 0 {
				t.Fatalf("late evaluation cancellation advanced control: %+v", event)
			}
			if timing.Search == 0 || timing.Rates == 0 || timing.Projection != 0 || timing.Exact == 0 || (timing.Surrogate > 0) != (strategy == "v2") {
				t.Fatalf("completed backend diagnostics lost on nominal-rate error: %+v", timing)
			}
		})
	}
}
