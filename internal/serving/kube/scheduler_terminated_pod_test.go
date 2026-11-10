package kube

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Supply the API state of a failed, deleting Pod that never acquired an executor.
// All removal, source retirement, preparation and route receipts remain real.
func terminatedPodController(t *testing.T) (*SchedulerController, serving.SchedulerInstance) {
	t.Helper()
	c := handoffController(t)
	pod, _, _, _ := observerSourceFixture()
	pod.Name, pod.UID, pod.ResourceVersion = "failed-without-executor", "", ""
	pod.Spec.NodeName = "b"
	pod.Annotations[servingstate.ServingProfileAnnotation] = "b-profile"
	pod.Finalizers = []string{servingstate.SourceFinalizer}
	uid := c.config.Settings.AutonomousWorkloads[0]
	pod.OwnerReferences = []meta.OwnerReference{{APIVersion: "edge.dacs.io/v1alpha1", Kind: "Workload", Name: "workload", UID: types.UID(uid)}}
	if err := c.store.client.Create(c.ctx, pod); err != nil {
		t.Fatal(err)
	}
	// The shared creation fixture initially publishes a running status. Replace
	// that API-only fixture state before the first observation of this Pod.
	pod.Status = core.PodStatus{Phase: core.PodFailed}
	if err := c.store.client.Status().Update(c.ctx, pod); err != nil {
		t.Fatal(err)
	}
	c.observer.mu.Lock()
	source := c.observer.sources[string(pod.UID)]
	source.containerID = ""
	c.observer.mu.Unlock()
	api := c.store.client.(*resetAPI)
	api.terminateOnDelete = false
	if err := api.Delete(c.ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := c.kube.CoreV1().Services(pod.Namespace).Create(c.ctx, observerServiceFixture(pod), meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.client.Create(c.ctx, &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: pod.Namespace, Name: pod.Name}}); err != nil {
		t.Fatal(err)
	}
	snapshot := handoffSnapshot(t, c)
	index := slices.IndexFunc(snapshot.Instances, func(i serving.SchedulerInstance) bool { return i.PodUID == string(pod.UID) })
	if index < 0 {
		t.Fatal("failed Pod disappeared from physical inventory")
	}
	old := snapshot.Instances[index]
	if old.ID != old.PodUID || !old.Stopped || !old.Terminated || old.Ready || old.Allocation.MemoryBytes == 0 {
		t.Fatalf("fixture lacks verified Pod-only occupancy: %+v", old)
	}
	return c, old
}

func TestTerminatedPodOnlyPlannerReachesNormalRemovePrepareAndRoute(t *testing.T) {
	c, old := terminatedPodController(t)
	snapshot := c.state.Snapshot
	uid := snapshot.Workloads[0].UID
	// Isolate the real planner's one-node replacement choice. The controller
	// and operations still see the complete physical inventory during execution.
	snapshot.Nodes = slices.DeleteFunc(slices.Clone(snapshot.Nodes), func(n serving.SchedulerNode) bool { return n.Node != old.Node })
	snapshot.Nodes[0].MemoryBytes = old.Allocation.MemoryBytes
	snapshot.Instances = []serving.SchedulerInstance{old}
	snapshot.Options = slices.DeleteFunc(slices.Clone(snapshot.Options), func(o serving.SchedulerOption) bool { return o.Instance.Node != old.Node })
	snapshot.Routing = map[string]map[string]int32{uid: {}}
	snapshot.ObservedSince = snapshot.ObservationEnd.Add(-c.config.Settings.ObservationWindow)
	for index := range 5 {
		snapshot.Arrivals = append(snapshot.Arrivals, serving.SchedulerArrival{ID: string(rune('a' + index)), Definition: snapshot.Workloads[0], Compute: true,
			At: snapshot.ObservationEnd.Add(-80*time.Millisecond + time.Duration(index)*10*time.Millisecond)})
	}
	c.config.Settings.ComparisonPeriod = time.Second
	c.config.Settings.MinimumDemand = map[string]float64{uid: 1}
	c.config.Settings.DefaultOperationCost[servingstate.OperationRemove] = serving.OperationCostValue{Seconds: .001, Joules: .01}
	decision, err := serving.EvaluateScheduler(c.ctx, c.config.Settings, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Target == nil || decision.Target.Keep {
		t.Fatalf("planner did not replace terminated occupancy: reasons=%v capacity=%+v", decision.Reasons, decision.Capacity)
	}
	goal := decision.Target
	var remove, prepare, route serving.Action
	for _, action := range goal.Actions {
		switch action.Kind {
		case servingstate.OperationRemove:
			remove = action
		case servingstate.OperationPrepare:
			prepare = action
		case servingstate.OperationRoute:
			route = action
		case servingstate.OperationStop, servingstate.OperationDrain:
			t.Fatalf("Pod-only cleanup acquired an executor chain: %+v", goal.Actions)
		}
	}
	if remove.Instance != old.ID || prepare.ID == "" || route.ID == "" ||
		!slices.Contains(prepare.DependsOn, remove.ID) || !slices.Contains(route.DependsOn, prepare.ID) {
		t.Fatalf("planner lost physical-return ordering: %+v", goal.Actions)
	}

	// Hold the real finalizer return. No successful remove receipt or subsequent
	// prepare/route may appear while the physical source remains in the API.
	reached, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	api := c.store.client.(*resetAPI)
	api.Client = interceptor.NewClient(api.Client.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}
			if string(obj.GetUID()) == old.PodUID && bytes.Contains(data, []byte("$deleteFromPrimitiveList/finalizers")) {
				close(reached)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return cl.Patch(ctx, obj, patch, opts...)
		},
	})
	c.launch(c.ctx, *goal, c.config)
	select {
	case <-reached:
	case <-c.ctx.Done():
		t.Fatal("normal remove did not reach finalizer return", c.ctx.Err())
	}
	c.mu.Lock()
	run := c.runs[remove.ID]
	if run == nil || run.action.Status != servingstate.NativeOperationRunning || run.result.Operation.Instance != "" || run.result.Operation.PodUID != old.PodUID ||
		c.runs[prepare.ID] != nil || c.runs[route.ID] != nil || c.dependenciesReady(prepare, *goal, c.runtime) || !c.runs[transitionRunKind+"/"+goal.ID].reserved {
		c.mu.Unlock()
		t.Fatal("Pod-only translation or physical-return fence failed")
	}
	c.mu.Unlock()
	if !slices.ContainsFunc(c.runtime.Instances(), func(i serving.InstanceState) bool { return i.PodUID == old.PodUID }) {
		t.Fatal("allocation returned before finalizer/Pod removal")
	}
	close(release)
	c.wg.Wait()
	requireCompletedContinuationAction(t, c.runs[remove.ID], "actual Pod-only remove")
	key := client.ObjectKey{Namespace: "chill-system", Name: "failed-without-executor"}
	requireRetiredHandoffResources(t, c, key, old)
	if slices.ContainsFunc(c.runtime.Instances(), func(i serving.InstanceState) bool { return i.PodUID == old.PodUID }) {
		t.Fatal("successful normal remove retained physical allocation")
	}
	snapshot = handoffSnapshot(t, c)
	operation := scheduledOperation(prepare, goal, c.config, c.powerTargets, snapshot.Workloads)
	if operation.Prepare == nil {
		t.Fatal("planner target has no preparation contract")
	}
	nextHTTP, _ := preparationNativeHTTPClient(t, c.ctx, operation.Prepare.Config, *operation.Prepare.Instance.Profile)
	c.observer.native.HTTPClient = nextHTTP
	handoffDispatch(t, c, goal, prepare)
	requireCompletedContinuationAction(t, c.runs[prepare.ID], "actual replacement prepare")
	handoffSnapshot(t, c)
	handoffDispatch(t, c, goal, route)
	requireCompletedContinuationAction(t, c.runs[route.ID], "actual replacement route")
	physical := c.runs[prepare.ID].prepareIdentity
	routing, _ := c.runtime.SchedulerRouting()
	if physical == "" || routing[uid][physical] <= 0 {
		t.Fatalf("replacement never became a positive applied route: %v", routing)
	}
}

func TestTerminatedPodOnlyRemoveRetainsChangedAndUnknownSources(t *testing.T) {
	for _, mismatch := range []string{"action-pod", "snapshot-pod", "node-uid", "replaced-pod", "replaced-node", "unknown", "revived", "retained-executor"} {
		t.Run(mismatch, func(t *testing.T) {
			c, old := terminatedPodController(t)
			action := serving.Action{ID: "guarded-remove", Kind: servingstate.OperationRemove, Node: old.Node, Instance: old.ID}
			index := slices.IndexFunc(c.state.Snapshot.Instances, func(i serving.SchedulerInstance) bool { return i.ID == old.ID })
			switch mismatch {
			case "action-pod":
				action.Instance = "different-pod"
			case "snapshot-pod":
				c.state.Snapshot.Instances[index].PodUID = "different-pod"
			case "node-uid":
				action.Node.UID = "replaced-node"
			case "unknown":
				c.state.Snapshot.Instances[index].Terminated = false
			case "revived":
				pod, err := c.kube.CoreV1().Pods("chill-system").Get(c.ctx, "failed-without-executor", meta.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				pod.Status = core.PodStatus{Phase: core.PodRunning, ContainerStatuses: []core.ContainerStatus{{Name: servingstate.FirstfloorContainerName, ContainerID: "containerd://new", State: core.ContainerState{Running: &core.ContainerStateRunning{}}}}}
				if err := c.kube.(*kubefake.Clientset).Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace); err != nil {
					t.Fatal(err)
				}
			case "replaced-pod":
				pod, err := c.kube.CoreV1().Pods("chill-system").Get(c.ctx, "failed-without-executor", meta.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				pod.UID = "different-pod"
				if err := c.kube.(*kubefake.Clientset).Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace); err != nil {
					t.Fatal(err)
				}
			case "replaced-node":
				node, err := c.kube.CoreV1().Nodes().Get(c.ctx, old.Node.Name, meta.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				node.UID = "different-node"
				if err := c.kube.(*kubefake.Clientset).Tracker().Update(core.SchemeGroupVersion.WithResource("nodes"), node, ""); err != nil {
					t.Fatal(err)
				}
			case "retained-executor":
				c.observer.mu.Lock()
				c.observer.sources[old.PodUID].containerID, c.observer.sources[old.PodUID].stopped = "containerd://unknown", false
				c.observer.mu.Unlock()
			}
			goal := serving.SchedulerCandidate{ID: "guarded-cleanup", Actions: []serving.Action{action}}
			handoffDispatch(t, c, &goal, action)
			if run := c.runs[action.ID]; run != nil && run.action.Status == servingstate.NativeOperationComplete {
				t.Fatal("changed or unknown source was reported removed")
			}
			pod, err := c.kube.CoreV1().Pods("chill-system").Get(c.ctx, "failed-without-executor", meta.GetOptions{})
			if err != nil || !slices.Contains(pod.Finalizers, servingstate.SourceFinalizer) || !slices.ContainsFunc(c.runtime.Instances(), func(i serving.InstanceState) bool { return i.PodUID == old.PodUID }) {
				t.Fatalf("guard discarded physical source/finalizer/allocation: pod=%+v err=%v", pod, err)
			}
		})
	}
}
