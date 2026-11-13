package kube

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

// 준비된 인스턴스 분배, 실패 격리와 관측 기반 복구, Pod별 실물 자원 장부: a frozen route chain progresses without Tick or a new decision.
func TestCompletionContinuesFrozenDAGWithoutPeriodicTick(t *testing.T) {
	for _, scenario := range []string{"complete", "duplicate-completion", "cancel-owner", "controls-off", "uid-off", "capture-cancelled", "capture-failed", "late-ack", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			ackPending, releaseACK := make(chan struct{}), make(chan struct{})
			var armed atomic.Bool
			var once sync.Once
			c, prepared := tickReadySubsetController(t, func(_ int, _ *native.State) {
				if scenario == "late-ack" && armed.Load() {
					once.Do(func() {
						close(ackPending)
						<-releaseACK
					})
				}
			})
			prepared.Store(true)
			c.completions = make(chan struct{}, 1)
			c.config.Settings.Controls.Routing = true
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			snapshot, err := c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			uid := snapshot.Workloads[0].UID
			instance := snapshot.Instances[0]
			instance.WorkloadUID = uid
			goal := serving.SchedulerCandidate{ID: "completion-chain", Nodes: snapshot.Nodes,
				Instances: []serving.SchedulerInstance{instance}, Routing: map[string]map[string]int32{uid: {instance.ID: 1000}},
				Actions: []serving.Action{{ID: "first-route", Kind: servingstate.OperationRoute}, {ID: "second-route", Kind: servingstate.OperationRoute, DependsOn: []string{"first-route"}}}}
			c.runtime.EnableSchedulerRouting()
			installTestTransition(c, &goal)
			parent := c.runs[transitionRunKind+"/"+goal.ID]
			parent.admittedControls = c.config.Settings.Controls
			c.state.Snapshot = snapshot
			armed.Store(true)
			c.mu.Lock()
			c.continueTransitionsLocked(c.config.Settings, true, func(target serving.SchedulerCandidate) {
				c.launchTargetLocked(c.ctx, target, c.config)
			})
			c.mu.Unlock()
			if scenario == "late-ack" {
				select {
				case <-ackPending:
				case <-ctx.Done():
					close(releaseACK)
					t.Fatal("native ACK did not reach the response boundary")
				}
				c.mu.Lock()
				parent.action.Status = servingstate.NativeOperationCancel
				c.mu.Unlock()
				close(releaseACK)
			}
			select {
			case <-c.Completions():
			case <-ctx.Done():
				t.Fatal("action completion did not wake continuation")
			}
			c.wg.Wait()
			first := c.runs["first-route"]
			requireCompletedContinuationAction(t, first, "first route")
			switch scenario {
			case "cancel-owner":
				parent.action.Status = servingstate.NativeOperationCancel
			case "controls-off":
				c.config.Settings.Controls.Routing = false
			case "uid-off":
				c.config.Settings.AutonomousWorkloads = nil
			case "capture-cancelled":
				cancel()
			case "capture-failed":
				if err := c.observer.Close(); err != nil {
					t.Fatal(err)
				}
			case "partial":
				first.action.Status, first.result.Status = servingstate.NativeOperationPartial, servingstate.NativeOperationPartial
			}
			before := c.State().Decision
			err = c.Continue(ctx, time.Now())
			if scenario == "capture-cancelled" || scenario == "capture-failed" {
				if err == nil {
					t.Fatal("bad observation unexpectedly authorized continuation")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			second := c.runs["second-route"]
			if scenario != "complete" && scenario != "duplicate-completion" {
				if second != nil {
					t.Fatal("unsafe continuation launched dependent action")
				}
				return
			}
			requireCompletedContinuationAction(t, second, "frozen chain did not advance")
			if !c.State().Decision.At.Equal(before.At) {
				t.Fatal("continuation emitted a new decision")
			}
			if err := c.Continue(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			if c.runs["second-route"] != second {
				t.Fatal("duplicate completion relaunched an action")
			}
		})
	}
}

func requireCompletedContinuationAction(t *testing.T, run *actionRun, message string) {
	t.Helper()
	if run == nil || run.action.Status != servingstate.NativeOperationComplete {
		t.Fatalf("%s: %+v", message, run)
	}
}
