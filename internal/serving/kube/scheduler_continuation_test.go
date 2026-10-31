package kube

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

// SCH-20/26/50: a frozen route chain progresses without Tick or a new decision.
func TestCompletionContinuesFrozenDAGWithoutPeriodicTick(t *testing.T) {
	for _, scenario := range []string{"complete", "duplicate-completion", "cancel-owner", "controls-off", "uid-off", "freshness", "capture-failed", "late-ack", "partial"} {
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
				Actions: []serving.Action{{ID: "first-route", Kind: operationRoute}, {ID: "second-route", Kind: operationRoute, DependsOn: []string{"first-route"}}}}
			c.runtime.EnableSchedulerRouting()
			installTestTransition(c, &goal)
			parent := c.runs[transitionRunKind+"/"+goal.ID]
			parent.admittedControls = c.config.Settings.Controls
			c.state.Snapshot = snapshot
			armed.Store(true)
			c.mu.Lock()
			c.launchRunningParentsLocked(c.config, true)
			c.mu.Unlock()
			if scenario == "late-ack" {
				select {
				case <-ackPending:
				case <-ctx.Done():
					close(releaseACK)
					t.Fatal("native ACK did not reach the response boundary")
				}
				c.mu.Lock()
				parent.action.Status = nativeOperationCancel
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
			if first == nil || first.action.Status != nativeOperationComplete {
				t.Fatalf("first route: %+v", first)
			}
			switch scenario {
			case "cancel-owner":
				parent.action.Status = nativeOperationCancel
			case "controls-off":
				c.config.Settings.Controls.Routing = false
			case "uid-off":
				c.config.Settings.AutonomousWorkloads = nil
			case "freshness":
				c.config.Settings.MaxObservationAge = time.Nanosecond
			case "capture-failed":
				cancel()
			case "partial":
				first.action.Status, first.result.Status = nativeOperationPartial, nativeOperationPartial
			}
			before := c.State().Decision
			err = c.Continue(ctx, time.Now())
			if scenario == "freshness" || scenario == "capture-failed" {
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
			if second == nil || second.action.Status != nativeOperationComplete {
				t.Fatalf("frozen chain did not advance: %+v", second)
			}
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
