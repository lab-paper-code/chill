package kube

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A real failed Create has no new Pod identity. Its admission scope also carries
// an old occupant; observing that old Pod disappear cannot prove the new Create
// had no effect. An independently completed cleanup must still return its node.
func TestDefectELostCreateRetainsOwnUnknownResponsibility(t *testing.T) {
	c := handoffController(t)
	snapshot := handoffSnapshot(t, c)
	uid := snapshot.Workloads[0].UID
	unroute := serving.SchedulerCandidate{ID: "lost-create-unroute-seed", Routing: map[string]map[string]int32{uid: {}}}
	if _, err := c.operations.Execute(c.ctx, servingstate.Operation{ID: unroute.ID, Kind: servingstate.OperationUnroute, Target: &unroute}); err != nil {
		t.Fatal(err)
	}
	snapshot = handoffSnapshot(t, c)
	var old, independent serving.SchedulerInstance
	for _, instance := range snapshot.Instances {
		if instance.Node.Name == "a" {
			old = instance
		}
		if instance.Node.Name == "d" {
			independent = instance
		}
	}
	wanted := c.config.Preparations["a-profile"].Instance
	wanted.ID = "unknown-own-creation-slot"
	api := c.store.client.(*resetAPI)
	api.Client = interceptor.NewClient(api.Client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, object client.Object, options ...client.CreateOption) error {
			if pod, ok := object.(*core.Pod); ok && pod.Name == slotName(wanted.ID) {
				return errors.New("Pod Create response unavailable")
			}
			return cl.Create(ctx, object, options...)
		},
	})
	goal := serving.SchedulerCandidate{ID: "lost-create-owner", Instances: []serving.SchedulerInstance{wanted}, Routing: map[string]map[string]int32{uid: {}}, Actions: []serving.Action{
		{ID: "lost-create-prepare", Kind: servingstate.OperationPrepare, Node: wanted.Node, Instance: wanted.ID},
		{ID: "lost-create-independent-stop", Kind: servingstate.OperationStop, Node: independent.Node, Instance: independent.ID},
		{ID: "lost-create-independent-drain", Kind: servingstate.OperationDrain, Node: independent.Node, Instance: independent.ID, DependsOn: []string{"lost-create-independent-stop"}},
		{ID: "lost-create-independent-remove", Kind: servingstate.OperationRemove, Node: independent.Node, Instance: independent.ID, DependsOn: []string{"lost-create-independent-drain"}},
	}}
	c.launch(c.ctx, goal, c.config)
	c.wg.Wait()
	child := c.runs[goal.Actions[0].ID]
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	if child == nil || child.result == nil || child.action.Status != servingstate.NativeOperationFailed || child.result.Operation.PodUID != "" || child.result.Preparation != nil || child.result.EffectsNotStarted || !slices.Contains(child.scope.Pods, old.PodUID) {
		t.Fatalf("actual reserve/Create error did not retain ambiguous own creation plus old occupant scope: %+v", child)
	}
	// Supply genuine retirement of the previous exact executor, not a fabricated
	// empty scope or an own-creation receipt for the failed prepare.
	if result, err := c.operations.Execute(c.ctx, servingstate.Operation{ID: "retire-preexisting-after-lost-create", Kind: servingstate.OperationRemove, Node: old.Node, Instance: old.ID, PodUID: old.PodUID}); err != nil {
		t.Fatalf("old exact executor cleanup: %v %+v", err, result)
	}
	for range 4 {
		if err := c.Continue(c.ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
	}
	observed := handoffSnapshot(t, c)
	if slices.ContainsFunc(observed.Instances, func(instance serving.SchedulerInstance) bool { return instance.PodUID == old.PodUID }) {
		t.Fatal("fresh capture still contains the previous Pod")
	}
	if !parent.reserved || !child.reserved || !slices.Contains(parent.scope.Nodes, old.Node) || !slices.Contains(child.scope.Nodes, old.Node) || c.authorize(servingstate.Operation{Kind: servingstate.OperationPrepare, Node: old.Node}) == nil {
		t.Fatal("unrelated old Pod absence returned unknown own creation or admitted a duplicate prepare")
	}
	requireCompletedContinuationAction(t, c.runs["lost-create-independent-remove"], "independent real cleanup")
	if slices.Contains(parent.scope.Nodes, independent.Node) || c.authorize(servingstate.Operation{Kind: servingstate.OperationPrepare, Node: independent.Node}) != nil {
		t.Fatal("unknown own creation prevented a proved independent node from returning")
	}
}
