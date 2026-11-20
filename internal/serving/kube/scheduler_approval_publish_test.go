package kube

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// The target's earlier transition failed one observation window ago and a Node
// the target acts on changed since evaluation. Approval must classify the
// target as new only after its own recapture retires that parent; deciding
// before would skip the planned-Node check and launch a fresh transition.
func TestSchedulerApprovalChecksTargetWhoseFailedParentRetiresDuringRecapture(t *testing.T) {
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
		Instances: slices.Clone(snapshot.Instances), Actions: []serving.Action{{ID: "retired-parent-route", Kind: servingstate.OperationRoute, Node: ready.Node}},
		Routing: map[string]map[string]int32{uid: {snapshot.Instances[0].ID: 1000, snapshot.Instances[1].ID: 0}}}
	evaluated := snapshot
	evaluated.Nodes = slices.Clone(snapshot.Nodes)
	for k := range evaluated.Nodes {
		if evaluated.Nodes[k].Node == ready.Node {
			evaluated.Nodes[k].PowerMode += "-changed"
		}
	}
	id := transitionRunKind + "/" + target.ID
	ended := time.Now().Add(-c.config.Settings.ObservationWindow - 50*time.Millisecond)
	c.mu.Lock()
	c.runs[id] = &actionRun{owner: target.ID, started: ended,
		action: serving.Action{ID: id, Kind: transitionRunKind, Status: servingstate.NativeOperationFailed},
		result: &servingstate.OperationResult{Operation: servingstate.Operation{ID: id, Kind: transitionRunKind, Target: &target},
			Status: servingstate.NativeOperationFailed, StartedAt: ended, EndedAt: ended}}
	c.mu.Unlock()
	c.tickMu.Lock()
	_, err = c.approveEvaluation(parent, time.Now(), c.config, snapshot.At, evaluated,
		serving.SchedulerDecision{At: snapshot.At, Target: &target}, &observation.SchedulerControlObservation{}, func(string, *time.Duration) {})
	c.tickMu.Unlock()
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if launched := c.runs[id] != nil && c.runs[id].action.Status != servingstate.NativeOperationFailed; launched ||
		err == nil || !strings.Contains(err.Error(), "planned Node changed") {
		t.Fatalf("fresh transition %s launched without the planned-Node check (approval error %v, route %+v)",
			id, err, c.runs["retired-parent-route"])
	}
}
