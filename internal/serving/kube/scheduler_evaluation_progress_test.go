package kube

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// Block the real evaluator at its context boundary, without a controller hook.
type heldEvaluationContext struct {
	context.Context
	entered, release chan struct{}
	once             sync.Once
	function         string
}

func (c *heldEvaluationContext) Err() error {
	pc, _, _, _ := runtime.Caller(1)
	function := c.function
	if function == "" {
		function = ".evaluateScheduler"
	}
	if fn := runtime.FuncForPC(pc); fn != nil && strings.HasSuffix(fn.Name(), function) {
		c.once.Do(func() {
			close(c.entered)
			<-c.release
		})
	}
	return c.Context.Err()
}

func holdSchedulerEvaluation(t *testing.T, c *SchedulerController) (<-chan error, func()) {
	t.Helper()
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ctx := &heldEvaluationContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	resume := func() { release.Do(func() { close(ctx.release) }) }
	done := make(chan error, 1)
	go func() { _, err := c.Tick(ctx, time.Now()); done <- err }()
	t.Cleanup(func() { resume(); cancel() })
	select {
	case <-ctx.entered:
	case err := <-done:
		t.Fatalf("Tick did not enter evaluation: %v", err)
	case <-parent.Done():
		t.Fatal(parent.Err())
	}
	return done, resume
}

func TestSchedulerEvaluationDoesNotBlockContinuationAndPublishesLatestCapture(t *testing.T) {
	c := schedulerEmptyPoolController(t)
	done, resume := holdSchedulerEvaluation(t, c)
	continued := make(chan error, 1)
	go func() { continued <- c.Continue(context.Background(), time.Now()) }()
	select {
	case err := <-continued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		resume()
		<-done
		<-continued
		t.Fatal("Continue waited for the running evaluation")
	}
	before := c.State().Snapshot
	resume()
	if err := <-done; err != nil {
		t.Fatalf("progress during evaluation discarded the Keep decision: %v", err)
	}
	if state := c.State(); !state.Snapshot.At.After(before.At) || state.Decision.At.IsZero() {
		t.Fatalf("Keep decision was not published on a capture after progress: %s -> %s", before.At, state.Snapshot.At)
	}
}

func TestSchedulerEvaluationRejectsChangedConfiguration(t *testing.T) {
	c := schedulerEmptyPoolController(t)
	done, resume := holdSchedulerEvaluation(t, c)
	settings := c.Settings()
	settings.Controls.Routing = false
	accepted := make(chan error, 1)
	go func() { accepted <- c.AcceptSettings(context.Background(), settings, nil) }()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		resume()
		<-done
		<-accepted
		t.Fatal("settings acceptance waited for evaluation")
	}
	resume()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "evaluation invalidated: configuration changed") {
		t.Fatalf("changed settings admitted the old evaluation: %v", err)
	}
	if !c.State().Decision.At.IsZero() {
		t.Fatal("invalidated evaluation published a decision")
	}
}

func TestSchedulerSnapshotPublicationNeverMovesBackwards(t *testing.T) {
	c := &reservationState{}
	latest := serving.SchedulerSnapshot{At: time.Now()}
	c.publishSnapshotLocked(&latest)
	old := serving.SchedulerSnapshot{At: latest.At.Add(-time.Second)}
	c.publishSnapshotLocked(&old)
	if !c.state.Snapshot.At.Equal(latest.At) {
		t.Fatal("snapshot publication moved time backwards")
	}
}

func TestSchedulerEvaluationAllowsPrepareToRouteProgress(t *testing.T) {
	c, prepared := tickReadySubsetController(t, nil)
	c.config.Settings.Controls.Routing = true
	c.runtime.EnableSchedulerRouting()
	snapshot, err := c.capture(context.Background(), time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	uid := snapshot.Workloads[0].UID
	destination := snapshot.Instances[1]
	destination.WorkloadUID = uid
	prepare := serving.Action{ID: "preparing-during-evaluation", Kind: servingstate.OperationPrepare,
		Node: destination.Node, Instance: destination.ID, Status: servingstate.NativeOperationRunning}
	route := serving.Action{ID: "route-after-prepare", Kind: servingstate.OperationRoute, DependsOn: []string{prepare.ID}}
	target := serving.SchedulerCandidate{ID: "progress-during-evaluation", Nodes: snapshot.Nodes,
		Instances: []serving.SchedulerInstance{destination}, Routing: map[string]map[string]int32{uid: {destination.ID: 1000}},
		Actions: []serving.Action{prepare, route}}
	c.state.Snapshot = snapshot
	installTestTransition(c, &target)
	c.runs[transitionRunKind+"/"+target.ID].admittedControls = c.config.Settings.Controls
	run := &actionRun{action: prepare, owner: target.ID, started: time.Now(),
		result: &servingstate.OperationResult{Operation: servingstate.Operation{ID: prepare.ID, Kind: prepare.Kind,
			Prepare: &servingstate.PrepareSpec{Instance: destination}}, Status: servingstate.NativeOperationRunning}}
	c.runs[prepare.ID] = run
	done, resume := holdSchedulerEvaluation(t, c)
	prepared.Store(true)
	receipt := *run.result
	receipt.Status, receipt.EndedAt = servingstate.NativeOperationComplete, time.Now()
	c.completeRun(run, &receipt, true, nil, c.ctx)
	continued := make(chan error, 1)
	go func() { continued <- c.Continue(context.Background(), time.Now()) }()
	select {
	case err := <-continued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		resume()
		<-done
		<-continued
		t.Fatal("prepare to route waited for evaluation")
	}
	c.wg.Wait()
	c.mu.Lock()
	routed := c.runs[route.ID]
	if routed == nil || routed.action.Status != servingstate.NativeOperationComplete {
		c.mu.Unlock()
		t.Fatalf("approved prepare did not progress to acknowledged route: %+v", routed)
	}
	at := c.state.Snapshot.At
	fence := c.routeWriters[uid][destination.ID]
	c.mu.Unlock()
	resume()
	if err := <-done; err != nil {
		t.Fatalf("progress during evaluation discarded it: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Snapshot.At.Before(at) || !c.routeWriters[uid][destination.ID].Equal(fence) || fence.IsZero() {
		t.Fatal("evaluation published a snapshot older than progress or erased its writer fence")
	}
}

func TestSchedulerTargetAdmissionDoesNotBlockProgress(t *testing.T) {
	c := schedulerEmptyPoolController(t)
	snapshot, err := c.capture(context.Background(), time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx := &heldEvaluationContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{}),
		function: ".SchedulerTargetAdmission"}
	var release sync.Once
	resume := func() { release.Do(func() { close(ctx.release) }) }
	defer resume()
	config := c.config
	target := serving.SchedulerCandidate{ID: "admitted-during-progress", Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: snapshot.Routing,
		Actions: []serving.Action{{ID: "admitted-route", Kind: servingstate.OperationRoute}}}
	done := make(chan error, 1)
	go func() {
		c.tickMu.Lock()
		_, err := c.approveEvaluation(ctx, time.Now(), config, snapshot.At, snapshot, serving.SchedulerDecision{Target: &target},
			&observation.SchedulerControlObservation{}, func(string, *time.Duration) {})
		c.tickMu.Unlock()
		done <- err
	}()
	select {
	case <-ctx.entered:
	case <-parent.Done():
		t.Fatal(parent.Err())
	}
	continued := make(chan error, 1)
	go func() { continued <- c.Continue(parent, time.Now()) }()
	select {
	case err := <-continued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		resume()
		<-done
		<-continued
		t.Fatal("Continue waited for target admission replay")
	}
	at := c.State().Snapshot.At
	resume()
	if err := <-done; err != nil {
		t.Fatalf("progress during admission discarded an admitted target: %v", err)
	}
	if c.State().Snapshot.At.Before(at) {
		t.Fatal("target approval published a snapshot older than progress")
	}
}

// The evaluation input predates an unrelated owner's completion and a newer
// published capture. Approval asks admission on the latest capture only.
func TestSchedulerApprovalAdmitsTargetOnLatestCapture(t *testing.T) {
	const reservedDuringAdmission = "reserved-during-admission"
	for _, change := range []string{"unrelated-completion", "instance-changed", reservedDuringAdmission} {
		t.Run(change, func(t *testing.T) {
			c, _ := tickReadySubsetController(t, nil)
			c.config.Settings.AutonomousWorkloads = nil
			c.config.Settings.Controls.Routing = true
			c.runtime.EnableSchedulerRouting()
			parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			snapshot, err := c.captureReconciled(parent, time.Now(), c.config, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			uid := snapshot.Workloads[0].UID
			ready := snapshot.Instances[0]
			ready.WorkloadUID = uid
			seed := serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{ready},
				Routing: map[string]map[string]int32{uid: {ready.ID: 1000}}}
			if err := c.runtime.ApplySchedulerWorkloadsTarget(seed, []string{uid}); err != nil {
				t.Fatal(err)
			}
			slow := serving.NodeReference{Name: slowNodeName, UID: slowNodeName}
			c.mu.Lock()
			c.runs["unrelated"] = &actionRun{owner: "unrelated", reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{slow}},
				action: serving.Action{ID: "unrelated", Kind: measurementLeaseKind}, result: &servingstate.OperationResult{}}
			c.mu.Unlock()
			snapshot, err = c.captureReconciled(parent, time.Now(), c.config, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			c.state.Snapshot = snapshot
			target := serving.SchedulerCandidate{ID: "latest-capture-target", Nodes: snapshot.Nodes,
				Instances: slices.Clone(snapshot.Instances), Actions: []serving.Action{{ID: "latest-capture-route", Kind: servingstate.OperationRoute}},
				Routing: map[string]map[string]int32{uid: {snapshot.Instances[0].ID: 1000, snapshot.Instances[1].ID: 0}}}
			if change == "instance-changed" {
				target.Instances[0].Allocation.MemoryBytes++
			}
			// During evaluation the unrelated owner completes and progress publishes.
			c.mu.Lock()
			c.releaseOwnerLocked("unrelated")
			c.mu.Unlock()
			if err := c.Continue(parent, time.Now()); err != nil {
				t.Fatal(err)
			}
			ctx := &heldEvaluationContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{}),
				function: ".SchedulerTargetAdmission"}
			if change != reservedDuringAdmission {
				close(ctx.release)
			}
			config := c.config
			done := make(chan error, 1)
			go func() {
				c.tickMu.Lock()
				_, err := c.approveEvaluation(ctx, time.Now(), config, snapshot.At, snapshot,
					serving.SchedulerDecision{At: snapshot.At, Target: &target}, &observation.SchedulerControlObservation{}, func(string, *time.Duration) {})
				c.tickMu.Unlock()
				done <- err
			}()
			if change == reservedDuringAdmission {
				<-ctx.entered
				c.mu.Lock()
				c.runs["taker"] = &actionRun{owner: "taker", reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{ready.Node}},
					action: serving.Action{ID: "taker", Kind: measurementLeaseKind}, result: &servingstate.OperationResult{}}
				c.mu.Unlock()
				close(ctx.release)
			}
			err = <-done
			c.wg.Wait()
			c.mu.Lock()
			defer c.mu.Unlock()
			route := c.runs["latest-capture-route"]
			switch change {
			case "unrelated-completion":
				if err != nil || route == nil || route.action.Status != servingstate.NativeOperationComplete {
					t.Fatalf("admissible target was not approved and launched: %v %+v", err, route)
				}
			case "instance-changed":
				if err == nil || !strings.Contains(err.Error(), "latest target admission: TargetWorkloadOutsideControl") || route != nil {
					t.Fatalf("target for a changed instance was not rejected by admission: %v", err)
				}
			case reservedDuringAdmission:
				if err == nil || !strings.Contains(err.Error(), "live reservations changed") || route != nil {
					t.Fatalf("target admitted before a reservation was launched: %v", err)
				}
			}
		})
	}
}
