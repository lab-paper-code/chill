package kube

import (
	"context"
	"slices"
	"strings"
	"testing"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// control402 orin-nano: the `profile` init container exited 1, so the
// attempt's Pod never acquired a container identity. Its reservation awaited
// that identity forever, compensation retired only Ready Pods, and the dependent
// handoff kept the owner's Workloads reserved against every later operation.
func TestFailedInitPrepareReleasesTransitionAfterPodRemoval(t *testing.T) {
	c := handoffController(t)
	snapshot := handoffSnapshot(t, c)
	uid := snapshot.Workloads[0].UID
	source := snapshot.Instances[slices.IndexFunc(snapshot.Instances, func(i serving.SchedulerInstance) bool { return i.Node.Name == "a" })]
	api := c.store.client.(*resetAPI)
	// No firstfloor container exists to terminate on DELETE.
	api.terminateOnDelete = false
	api.Client = interceptor.NewClient(api.Client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if pod, ok := obj.(*core.Pod); ok {
				// The empty `profile: Error (exit 1)` had no cause: init steps lacked the log fallback.
				for _, init := range pod.Spec.InitContainers {
					if init.TerminationMessagePolicy != core.TerminationMessageFallbackToLogsOnError {
						t.Errorf("init container %s does not record its log on failure", init.Name)
					}
				}
				pod.Status = core.PodStatus{Phase: core.PodFailed, InitContainerStatuses: []core.ContainerStatus{{Name: "profile",
					State: core.ContainerState{Terminated: &core.ContainerStateTerminated{ExitCode: 1, Reason: "Error", StartedAt: meta.Now(), FinishedAt: meta.Now()}}}}}
			}
			return cl.Create(ctx, obj, opts...)
		},
	})
	replacement := c.config.Preparations["b-profile"].Instance
	goal := &serving.SchedulerCandidate{ID: "failed-init-owner", Instances: []serving.SchedulerInstance{replacement},
		Routing: map[string]map[string]int32{uid: {replacement.ID: 1000}}, Actions: []serving.Action{
			{ID: "prepare-replacement", Kind: servingstate.OperationPrepare, Instance: replacement.ID, Node: replacement.Node},
			{ID: "handoff", Kind: servingstate.OperationRoute, DependsOn: []string{"prepare-replacement"}},
			{ID: "stop-source", Kind: servingstate.OperationStop, Instance: source.ID, Node: source.Node, DependsOn: []string{"handoff"}},
			{ID: "drain-source", Kind: servingstate.OperationDrain, Instance: source.ID, Node: source.Node, DependsOn: []string{"stop-source"}},
		}}
	handoffDispatch(t, c, goal, goal.Actions[0])
	prepare := c.runs["prepare-replacement"]
	if prepare.action.Status != servingstate.NativeOperationPartial && prepare.action.Status != servingstate.NativeOperationFailed ||
		!strings.Contains(prepare.action.Reason, "profile: Error (exit 1)") || prepare.result.Preparation == nil {
		t.Fatalf("fixture is not a failed init-container preparation: %+v", prepare.action)
	}
	pod := prepare.result.Preparation.PodUID
	unroute := servingstate.Operation{Kind: servingstate.OperationUnroute, WorkloadUID: uid}
	if c.authorize(unroute) == nil {
		t.Fatal("fixture owner did not reserve its Workload")
	}

	snapshot = handoffSnapshot(t, c)
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	if parent.action.Status != servingstate.NativeOperationFailed {
		t.Fatalf("failed preparation did not fail its transition: %+v", parent.action)
	}
	for _, action := range parent.result.Operation.Target.Actions[1:] {
		if action.Status != servingstate.NativeOperationCancel || action.Reason != prepare.action.Reason {
			t.Fatalf("dependent %s still awaits the failed instance: %+v", action.ID, action)
		}
	}
	if !slices.ContainsFunc(snapshot.Instances, func(i serving.SchedulerInstance) bool { return i.ID == pod && i.Terminated }) {
		t.Fatalf("failed Pod is not an identity-less terminated instance: %+v", snapshot.Instances)
	}

	c.launch(c.ctx, serving.SchedulerCandidate{}, c.config)
	c.wg.Wait()
	cleanup := c.runs["prepare-replacement/compensate/"+pod]
	if cleanup == nil || cleanup.action.Status != servingstate.NativeOperationComplete || cleanup.result.Operation.PodUID != pod {
		t.Fatalf("compensation did not remove the attempt's Pod by UID: %+v", cleanup)
	}
	if _, err := c.kube.CoreV1().Pods("chill-system").Get(c.ctx, prepare.result.Operation.Prepare.Name, meta.GetOptions{}); err == nil {
		t.Fatal("failed attempt's Pod remains")
	}

	snapshot = handoffSnapshot(t, c)
	if len(snapshot.Reservations) != 0 || parent.reserved {
		t.Fatalf("failed transition still reserves: %+v", snapshot.Reservations)
	}
	if err := c.authorize(unroute); err != nil {
		t.Fatal("manual unroute refused by the failed transition", err)
	}
	if err := c.authorize(servingstate.Operation{Kind: servingstate.OperationPrepare, Node: replacement.Node, WorkloadUIDs: []string{uid}}); err != nil {
		t.Fatal("new plan on the failed Node refused", err)
	}
}
