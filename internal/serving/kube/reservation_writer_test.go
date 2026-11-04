package kube

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRouteWriterSurvivesReturnAndLostReceipts(t *testing.T) {
	for _, outcome := range []struct {
		name, failAt  string
		forgetReceipt bool
		wantB         int32
	}{
		{name: "complete", wantB: 300},
		{name: "receipt-gc", forgetReceipt: true, wantB: 300},
		{name: "record-lost", failAt: servingstate.NativeOperationComplete, forgetReceipt: true, wantB: 300},
		{name: "before-effect-failure", failAt: starting},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			const ownerA, ownerB, ownerC = "writer-a", "writer-b", "writer-c"
			var foreignWrites atomic.Int32
			c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
				if index == 0 && state.Metadata.DecisionID == ownerB || index == 1 && state.Metadata.DecisionID == ownerA {
					foreignWrites.Add(1)
				}
			})
			prepared.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c.ctx = ctx
			snapshot, err := c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			c.state.Snapshot = snapshot
			uid := snapshot.Workloads[0].UID
			a, b := snapshot.Instances[0], snapshot.Instances[1]
			a.WorkloadUID, b.WorkloadUID = uid, uid
			readPolicy := func(name string) native.Policy {
				cm := &core.ConfigMap{}
				if err := c.store.client.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: name}, cm); err != nil {
					t.Fatal(err)
				}
				var cfg native.Config
				if err := json.Unmarshal([]byte(cm.Data["config.json"]), &cfg); err != nil {
					t.Fatal(err)
				}
				return cfg.Policy
			}
			aBeforeB := readPolicy("a")
			a.MaxWait, b.MaxWait = time.Millisecond, 2*time.Millisecond
			goalA := serving.SchedulerCandidate{ID: ownerA, Instances: []serving.SchedulerInstance{a},
				Routing: map[string]map[string]int32{uid: {a.ID: 1000}},
				Actions: []serving.Action{{ID: "route-writer-a", Kind: servingstate.OperationRoute, Node: a.Node}}}
			// This is the original admitted A continuation; it has not dispatched.
			c.startTransition(&goalA, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() {})
			goalB := serving.SchedulerCandidate{ID: ownerB, Instances: []serving.SchedulerInstance{snapshot.Instances[0], b},
				Routing: map[string]map[string]int32{uid: {a.ID: 700, b.ID: 300}},
				Actions: []serving.Action{{ID: "route-writer-b", Kind: servingstate.OperationRoute, Node: b.Node}}}
			goalB.Instances[0].WorkloadUID = uid
			c.config.OnAction = func(result servingstate.OperationResult) error {
				if result.Operation.Target == nil || result.Operation.Target.ID != ownerB {
					return nil
				}
				if result.Status == outcome.failAt {
					return errors.New("injected receipt persistence failure")
				}
				return nil
			}
			c.startTransition(&goalB, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() { c.launchTargetLocked(goalB, c.config) })
			c.wg.Wait()
			if run := c.runs[goalB.Actions[0].ID]; run == nil || run.result == nil {
				t.Fatal("B never reached the real operation dispatcher")
			}
			if afterB := readPolicy("a"); !reflect.DeepEqual(aBeforeB, afterB) {
				t.Fatalf("B overwrote A's native policy: before=%+v after=%+v", aBeforeB, afterB)
			}
			bBeforeA := readPolicy("b")
			snapshot, err = c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			c.state.Snapshot = snapshot
			c.releaseOwnerLocked(ownerB)
			if outcome.forgetReceipt {
				for id, run := range c.runs {
					if run.owner == ownerB {
						delete(c.runs, id)
					}
				}
			}
			c.launchTargetLocked(goalA, c.config)
			c.mu.Unlock()
			c.wg.Wait()
			applied, _ := c.runtime.SchedulerRouting()
			wantB := outcome.wantB
			if applied[uid][b.ID] != wantB || applied[uid][a.ID] != 1000-wantB {
				t.Fatalf("older continuation overwrote a newer committed share or fenced an unapplied write: %v", applied)
			}
			if afterA := readPolicy("b"); !reflect.DeepEqual(bBeforeA, afterA) {
				t.Fatalf("A overwrote B's native MaxBatch/MaxWait policy: before=%+v after=%+v", bBeforeA, afterA)
			}
			for _, actual := range c.runtime.Instances() {
				if actual.ID == b.ID && actual.Native != nil && !reflect.DeepEqual(actual.Native.Metadata.Policy, bBeforeA) {
					t.Fatalf("native B policy changed across foreign dispatch: %+v", actual.Native.Metadata.Policy)
				}
			}
			if foreignWrites.Load() != 0 {
				t.Fatal("foreign executor acknowledged another owner's policy write")
			}
			// Only a genuinely later admission C may replace B's committed share.
			snapshot, err = c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			c.state.Snapshot = snapshot
			c.releaseOwnerLocked(ownerA)
			c.mu.Unlock()
			a.MaxWait = 3 * time.Millisecond
			goalC := serving.SchedulerCandidate{ID: ownerC, Instances: []serving.SchedulerInstance{a}, Routing: map[string]map[string]int32{uid: {a.ID: 1000}}, Actions: []serving.Action{{ID: "route-writer-c", Kind: servingstate.OperationRoute, Node: a.Node}}}
			c.startTransition(&goalC, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() { c.launchTargetLocked(goalC, c.config) })
			c.wg.Wait()
			applied, _ = c.runtime.SchedulerRouting()
			if applied[uid][a.ID] != 1000 || applied[uid][b.ID] != 0 {
				t.Fatalf("genuinely later C remained fenced: %v", applied)
			}
		})
	}
}
