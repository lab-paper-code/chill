package kube

import (
	"slices"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The aggregate physical exclusion comes from actual Pod/native capture, not a
// manually added snapshot reason. An exact confirmed neighbor remains a valid
// routing destination; only node-wide physical work depends on all occupants.
func TestDefectEConfirmedDestinationWithUnknownNeighbor(t *testing.T) {
	const nodeNotReady, generatedSubset = "node-not-ready", "ready-subset"
	for _, scenario := range []string{"final-route", generatedSubset, nodeNotReady} {
		t.Run(scenario, func(t *testing.T) {
			c := handoffController(t)
			pod, err := c.kube.CoreV1().Pods("chill-system").Get(c.ctx, "c", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			neighbor := pod.DeepCopy()
			neighbor.Name, neighbor.UID, neighbor.ResourceVersion = "unconfirmed-coresident", "", ""
			delete(neighbor.Annotations, servingstate.ServingRoutingAnnotation)
			// The fake API assigns an exact Pod UID but no native Service or
			// policy ACK exists for this additional physical executor.
			if err := c.store.client.Create(c.ctx, neighbor); err != nil {
				t.Fatal(err)
			}
			if scenario == nodeNotReady {
				setDefectENodeNotReady(t, c, pod.Spec.NodeName)
			}
			snapshot := handoffSnapshot(t, c)
			uid := snapshot.Workloads[0].UID
			var healthy, unknown serving.SchedulerInstance
			for _, instance := range snapshot.Instances {
				if instance.PodUID == string(pod.UID) {
					healthy = instance
					healthy.WorkloadUID = uid
				}
				if instance.PodUID == string(neighbor.UID) {
					unknown = instance
				}
			}
			if scenario != nodeNotReady && !healthy.Ready || unknown.ID == "" || unknown.Ready || healthy.Node != unknown.Node || !slices.ContainsFunc(snapshot.Nodes, func(node serving.SchedulerNode) bool {
				return node.Node == healthy.Node && slices.Contains(node.Reasons, serving.NodeInstanceUnavailable)
			}) {
				t.Fatalf("actual capture did not preserve confirmed and unconfirmed co-residents: %+v", snapshot.Instances)
			}
			goal := serving.SchedulerCandidate{ID: "coresident-" + scenario, Instances: []serving.SchedulerInstance{healthy}, Routing: map[string]map[string]int32{uid: {healthy.ID: 1000}}, Actions: []serving.Action{
				{ID: "coresident-physical-stop", Kind: servingstate.OperationStop, Node: unknown.Node, Instance: unknown.ID},
				{ID: "coresident-route", Kind: servingstate.OperationRoute, Node: healthy.Node},
			}}
			if scenario == generatedSubset {
				unroute := serving.SchedulerCandidate{ID: "coresident-subset-seed", Routing: map[string]map[string]int32{uid: {}}}
				if _, err := c.operations.Execute(c.ctx, servingstate.Operation{ID: unroute.ID, Kind: servingstate.OperationUnroute, Target: &unroute}); err != nil {
					t.Fatal(err)
				}
				handoffSnapshot(t, c)
				goal.Actions[1].DependsOn = []string{goal.Actions[0].ID}
			}
			c.startTransition(&goal, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() {
				c.launchTargetLocked(goal, c.config)
			})
			c.wg.Wait()
			parent := c.runs[transitionRunKind+"/"+goal.ID]
			if c.runs[goal.Actions[0].ID] != nil {
				t.Fatal("co-resident unknown executor did not exclude node-wide physical changes")
			}
			if scenario == nodeNotReady {
				if c.runs[goal.Actions[1].ID] != nil {
					t.Fatal("confirmed destination bypassed its node's actual NotReady state")
				}
				return
			}
			if parent.action.Status == servingstate.NativeOperationFailed {
				t.Fatalf("unrelated unknown executor vetoed exact confirmed routing: %s", parent.action.Reason)
			}
			var route *actionRun
			for _, run := range c.runs {
				if run.owner == goal.ID && run.action.Kind == servingstate.OperationRoute {
					route = run
				}
			}
			requireCompletedContinuationAction(t, route, "confirmed co-resident native/runtime routing")
			if scenario == generatedSubset && route.action.From != readySubsetRoute {
				t.Fatal("dependent original route bypassed its physical dependency instead of generating the Ready subset")
			}
			applied, _ := c.runtime.SchedulerRouting()
			if applied[uid][healthy.ID] <= 0 || applied[uid][unknown.ID] != 0 {
				t.Fatalf("exact confirmed destination did not receive traffic independently: %v", applied)
			}
		})
	}
}

func setDefectENodeNotReady(t *testing.T, c *SchedulerController, name string) {
	t.Helper()
	node, err := c.kube.CoreV1().Nodes().Get(c.ctx, name, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == core.NodeReady {
			node.Status.Conditions[i].Status = core.ConditionFalse
		}
	}
	if _, err := c.kube.CoreV1().Nodes().UpdateStatus(c.ctx, node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}
