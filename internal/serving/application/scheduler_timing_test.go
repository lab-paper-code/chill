package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const schedulerTimingKeepStatus = "Keep"

func TestSchedulerControlRoundRetainsAdmissionLockAndTick(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		failedRecord, canceled bool
	}{
		{name: "keep"},
		{name: "failed-record", failedRecord: true},
		{name: "canceled-before-capture", canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := schedulerTimingService(t)
			failure := errors.New("controlled admission failure")
			var admitElapsed, recordElapsed time.Duration
			first := true
			service.kube.(*fake.Clientset).PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
				if !first {
					return false, nil, nil
				}
				first = false
				started := time.Now()
				time.Sleep(10 * time.Millisecond)
				admitElapsed = time.Since(started)
				return true, nil, failure
			})
			config := servingkube.SchedulerControllerConfig{Settings: service.Controller.Settings(), OnDecision: func(decision serving.SchedulerDecision, snapshot serving.SchedulerSnapshot) error {
				if tc.canceled || decision.Status != schedulerTimingKeepStatus || len(decision.Reasons) != 0 || len(snapshot.Nodes) != 1 || !snapshot.Nodes[0].Ready {
					t.Fatalf("expected evaluable empty Ready POOL: decision=%+v snapshot=%+v", decision, snapshot)
				}
				started := time.Now()
				time.Sleep(10 * time.Millisecond)
				recordElapsed = time.Since(started)
				if tc.failedRecord {
					return errors.New("controlled decision failure")
				}
				return nil
			}}
			if err := service.Controller.SetConfiguration(config); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			service.Lock.Lock()
			entered, done := make(chan struct{}), make(chan time.Time, 1)
			go func() {
				close(entered)
				done <- service.controlRound(ctx)
			}()
			<-entered
			time.Sleep(10 * time.Millisecond)
			service.Lock.Unlock()
			started := <-done
			rows, _, _, _, err := service.observationLog.SchedulerObservationPage(context.Background(), started, time.Now().Add(time.Second), 100, 0, observation.SchedulerObservationSelection{})
			if err != nil {
				t.Fatal(err)
			}
			round, tick := schedulerControlCompletionPair(t, rows)
			wantStatus := schedulerTimingKeepStatus
			if tc.canceled {
				wantStatus = ""
				if tick.Error != context.Canceled.Error() || tick.Phase != "capture" || tick.Record != 0 || tick.Launch != 0 {
					t.Fatalf("canceled Tick advanced beyond entry: %+v", tick)
				}
			}
			if !round.TickAt.Equal(tick.TickAt) || round.Status != "" || round.TargetID != "" || round.Fallback != "" || tick.Status != wantStatus || round.AdmitError != failure.Error() || (round.Error != "") != (tc.failedRecord || tc.canceled) {
				t.Fatalf("round disposition or join lost: round=%+v tick=%+v", round, tick)
			}
			if round.LockWait <= 0 || round.Admit < admitElapsed || tick.Record < recordElapsed || round.Tick < tick.Elapsed || round.Elapsed < round.LockWait+round.Admit+round.Tick {
				t.Fatalf("outer round omitted controlled work: round=%+v tick=%+v", round, tick)
			}
		})
	}
}

func schedulerControlCompletionPair(t *testing.T, rows []observation.SchedulerObservation) (round, tick *observation.SchedulerControlObservation) {
	t.Helper()
	for _, row := range rows {
		if row.Control == nil {
			continue
		}
		switch row.Control.Scope {
		case "round":
			round = row.Control
		case "tick":
			tick = row.Control
		}
	}
	if round == nil || tick == nil {
		t.Fatalf("round/Tick completion pair missing: %+v", rows)
	}
	return round, tick
}

func schedulerTimingService(t *testing.T) *SchedulerService {
	t.Helper()
	service := schedulerTestService(t, schedulerTestConfiguration())
	installPolicyControlSources(t, service)
	// Empty inventory is Pending (MissingPoolInventory). A Ready POOL
	// Node with no workloads is a real Keep without observation warm-up.
	node := &corev1.Node{
		ObjectMeta: meta.ObjectMeta{Name: "round-node", UID: "round-node-uid"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	if _, err := service.kube.CoreV1().Nodes().Create(context.Background(), node, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return service
}

func TestSchedulerControlRoundRecordedDecisionCancellation(t *testing.T) {
	service := schedulerTimingService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var decisionAt time.Time
	config := servingkube.SchedulerControllerConfig{Settings: service.Controller.Settings(), OnDecision: func(decision serving.SchedulerDecision, snapshot serving.SchedulerSnapshot) error {
		if err := service.RecordDecision(decision, snapshot); err != nil {
			return err
		}
		decisionAt = decision.At
		cancel()
		return nil
	}}
	if err := service.Controller.SetConfiguration(config); err != nil {
		t.Fatal(err)
	}
	started := service.controlRound(ctx)
	rows, _, _, _, err := service.observationLog.SchedulerObservationPage(context.Background(), started, time.Now().Add(time.Second), 100, 0, observation.SchedulerObservationSelection{})
	if err != nil {
		t.Fatal(err)
	}
	round, tick := schedulerControlCompletionPair(t, rows)
	var recorded *serving.SchedulerDecision
	for _, row := range rows {
		if row.Decision != nil {
			recorded = row.Decision
		}
	}
	if recorded == nil || decisionAt.IsZero() {
		t.Fatalf("recorded decision/completion chain missing: %+v", rows)
	}
	if !round.TickAt.Equal(tick.TickAt) || !tick.DecisionAt.Equal(decisionAt) || !recorded.At.Equal(decisionAt) || tick.Status != schedulerTimingKeepStatus || tick.TargetID == "" {
		t.Fatalf("recorded disposition lost after cancellation: round=%+v tick=%+v decision=%+v", round, tick, recorded)
	}
	if round.Status != "" || round.TargetID != "" || round.Fallback != "" || round.Error != context.Canceled.Error() || tick.Error != context.Canceled.Error() || tick.Phase != "record" || tick.Launch != 0 {
		t.Fatalf("canceled round inferred disposition or launched work: round=%+v tick=%+v", round, tick)
	}
}

func TestSchedulerControlRoundDoesNotHoldApplicationLockDuringEvaluation(t *testing.T) {
	service := schedulerTimingService(t)
	config := servingkube.SchedulerControllerConfig{Settings: service.Controller.Settings(),
		OnDecision: func(serving.SchedulerDecision, serving.SchedulerSnapshot) error {
			if !service.Lock.TryLock() {
				t.Error("control round held application lock through evaluation and recording")
			} else {
				service.Lock.Unlock()
			}
			return nil
		}}
	if err := service.Controller.SetConfiguration(config); err != nil {
		t.Fatal(err)
	}
	service.controlRound(context.Background())
}
