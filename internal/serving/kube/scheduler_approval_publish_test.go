package kube

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// Continue publishes a newer capture while target admission runs without
// tickMu. The ledger is unchanged, so the target is approved, persisted and
// launched; the launched decision must also be the published one.
func TestSchedulerApprovalPublishesLaunchedDecisionAfterProgressDuringAdmission(t *testing.T) {
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
	snapshot, err = c.captureReconciled(parent, time.Now(), c.config, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	target := serving.SchedulerCandidate{ID: "latest-capture-target", Nodes: snapshot.Nodes,
		Instances: slices.Clone(snapshot.Instances), Actions: []serving.Action{{ID: "latest-capture-route", Kind: servingstate.OperationRoute}},
		Routing: map[string]map[string]int32{uid: {snapshot.Instances[0].ID: 1000, snapshot.Instances[1].ID: 0}}}
	persisted := 0
	c.config.OnDecision = func(serving.SchedulerDecision, serving.SchedulerSnapshot) error { persisted++; return nil }
	ctx := &heldEvaluationContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{}),
		function: ".SchedulerTargetAdmission"}
	config := c.config
	done := make(chan error, 1)
	go func() {
		c.tickMu.Lock()
		_, err := c.approveEvaluation(ctx, time.Now(), config, snapshot.At, snapshot,
			serving.SchedulerDecision{At: snapshot.At, Target: &target}, &observation.SchedulerControlObservation{}, func(string, *time.Duration) {})
		c.tickMu.Unlock()
		done <- err
	}()
	<-ctx.entered
	// A completion signal runs Continue as soon as Tick releases tickMu.
	if err := c.Continue(parent, time.Now()); err != nil {
		t.Fatal(err)
	}
	close(ctx.release)
	err = <-done
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	route := c.runs["latest-capture-route"]
	if err != nil || route == nil {
		t.Fatalf("precondition: target was not approved and launched: %v %+v", err, route)
	}
	if persisted != 1 {
		t.Fatalf("precondition: decision persisted %d times", persisted)
	}
	if c.state.Decision.Target == nil || c.state.Decision.Target.ID != target.ID {
		got := "<nil>"
		if c.state.Decision.Target != nil {
			got = c.state.Decision.Target.ID
		}
		t.Fatalf("launched and persisted target %q is not the published decision (published target %s, at %s)",
			target.ID, got, c.state.Decision.At)
	}
}

// The target's earlier transition failed one observation window ago. Approval
// classifies the target as not new before its own recapture retires that
// parent, so launch starts a fresh transition that admission never saw.
func TestSchedulerApprovalAdmitsTargetWhoseFailedParentRetiresDuringRecapture(t *testing.T) {
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
	snapshot, err = c.captureReconciled(parent, time.Now(), c.config, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	target := serving.SchedulerCandidate{ID: "retired-parent-target", Nodes: snapshot.Nodes,
		Instances: slices.Clone(snapshot.Instances), Actions: []serving.Action{{ID: "retired-parent-route", Kind: servingstate.OperationRoute}},
		Routing: map[string]map[string]int32{uid: {snapshot.Instances[0].ID: 1000, snapshot.Instances[1].ID: 0}}}
	// Admission on the latest capture rejects this target (see instance-changed).
	target.Instances[0].Allocation.MemoryBytes++
	id := transitionRunKind + "/" + target.ID
	ended := time.Now().Add(-c.config.Settings.ObservationWindow - 50*time.Millisecond)
	c.mu.Lock()
	c.runs[id] = &actionRun{owner: target.ID, started: ended,
		action: serving.Action{ID: id, Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{ID: id, Kind: transitionRunKind, Target: &target},
			Status: servingstate.NativeOperationFailed, StartedAt: ended, EndedAt: ended}}
	c.mu.Unlock()
	ctx := &heldEvaluationContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{}),
		function: ".SchedulerTargetAdmission"}
	close(ctx.release)
	c.tickMu.Lock()
	_, err = c.approveEvaluation(ctx, time.Now(), c.config, snapshot.At, snapshot,
		serving.SchedulerDecision{At: snapshot.At, Target: &target}, &observation.SchedulerControlObservation{}, func(string, *time.Duration) {})
	c.tickMu.Unlock()
	c.wg.Wait()
	admitted := false
	select {
	case <-ctx.entered:
		admitted = true
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	launched := c.runs[id] != nil && c.runs[id].action.Status != servingstate.NativeOperationFailed
	if launched && !admitted {
		t.Fatalf("fresh transition %s launched without target admission (approval error %v, route %+v)",
			id, err, c.runs["retired-parent-route"])
	}
}
