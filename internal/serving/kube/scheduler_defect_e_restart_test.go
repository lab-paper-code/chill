package kube

import (
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// Replacing the volatile ledger does not change the persisted Pod identity,
// allocations or routing seeds, and cannot make unavailable occupancy removable.
func TestDefectENewLedgerDoesNotReformUnavailablePhysicalOwner(t *testing.T) {
	old := handoffController(t, "e", "f")
	lost := loseDefectEProbe(old, "10.96.0.15:8080")
	lost.Store(true)
	cluster := NewClusterState(old.store, old.kube, old.operations.Config.Namespace)
	operations, err := servingops.NewOperations(cluster, old.store, old.kube, old.runtime, old.observer, old.observer.native, old.operations.Config)
	if err != nil {
		t.Fatal(err)
	}
	c := &SchedulerController{reservationState: &cluster.reservation, store: old.store, kube: old.kube, runtime: old.runtime, observer: old.observer,
		operations: operations, observationLog: old.observationLog, config: old.config, ctx: old.ctx}
	t.Cleanup(func() { c.wg.Wait() })
	if len(c.runs) != 0 {
		t.Fatal("new ledger inherited volatile owner evidence")
	}
	if _, err := c.Tick(c.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	// The unavailable source stays physically held across later captures.
	time.Sleep(c.config.Settings.ObservationWindow + c.config.Settings.ControlInterval)
	decision, err := c.Tick(c.ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	unknown := slices.IndexFunc(c.State().Snapshot.Instances, func(instance serving.SchedulerInstance) bool { return instance.Node.Name == "f" })
	if unknown < 0 || decision.Target == nil {
		t.Fatal("persisted unavailable occupancy disappeared after new ledger capture")
	}
	physical := c.State().Snapshot.Instances[unknown]
	if !slices.ContainsFunc(decision.Target.Instances, func(instance serving.SchedulerInstance) bool {
		return instance.ID == physical.ID && instance.Node == physical.Node
	}) {
		t.Fatal("empty ledger became absence proof for unavailable executor")
	}
	for _, action := range decision.Target.Actions {
		if action.Kind != servingstate.OperationRoute && action.Node == physical.Node {
			t.Fatalf("new ledger reformed unavailable physical cleanup: %+v", action)
		}
	}
	for _, run := range c.runs {
		if slices.Contains(run.scope.Nodes, physical.Node) && run.action.Kind != servingstate.OperationRoute {
			t.Fatalf("new unavailable physical owner acquired healthy siblings: %+v", run.action)
		}
	}
}
