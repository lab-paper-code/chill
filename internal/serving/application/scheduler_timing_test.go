package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSchedulerControlRoundRetainsAdmissionLockAndTick(t *testing.T) {
	for _, failedRecord := range []bool{false, true} {
		name := "keep"
		if failedRecord {
			name = "failed-record"
		}
		t.Run(name, func(t *testing.T) {
			service := schedulerTestService(t, schedulerTestConfiguration())
			installPolicyControlSources(t, service)
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
			config := servingkube.SchedulerControllerConfig{Settings: service.Controller.Settings(), OnDecision: func(serving.SchedulerDecision, serving.SchedulerSnapshot) error {
				started := time.Now()
				time.Sleep(10 * time.Millisecond)
				recordElapsed = time.Since(started)
				if failedRecord {
					return errors.New("controlled decision failure")
				}
				return nil
			}}
			if err := service.Controller.SetConfiguration(config); err != nil {
				t.Fatal(err)
			}
			service.Lock.Lock()
			entered, done := make(chan struct{}), make(chan time.Time, 1)
			go func() {
				close(entered)
				done <- service.controlRound(context.Background())
			}()
			<-entered
			time.Sleep(10 * time.Millisecond)
			service.Lock.Unlock()
			started := <-done
			rows, _, _, _, err := service.observationLog.SchedulerObservationPage(context.Background(), started, time.Now().Add(time.Second), 100, 0, observation.SchedulerObservationSelection{})
			if err != nil {
				t.Fatal(err)
			}
			var round, tick *observation.SchedulerControlObservation
			for _, row := range rows {
				if row.Control == nil {
					continue
				}
				if row.Control.Scope == "round" {
					round = row.Control
				} else if row.Control.Scope == "tick" {
					tick = row.Control
				}
			}
			if round == nil || tick == nil {
				t.Fatalf("round/Tick completion pair missing: %+v", rows)
			}
			if !round.TickAt.Equal(tick.TickAt) || round.Status != "Keep" || round.AdmitError != failure.Error() || (round.Error != "") != failedRecord {
				t.Fatalf("round disposition or join lost: round=%+v tick=%+v", round, tick)
			}
			if round.LockWait <= 0 || round.Admit < admitElapsed || tick.Record < recordElapsed || round.Tick < tick.Elapsed || round.Elapsed < round.LockWait+round.Admit+round.Tick {
				t.Fatalf("outer round omitted controlled work: round=%+v tick=%+v", round, tick)
			}
		})
	}
}
