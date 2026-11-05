package kube

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type recoveryBootObservation struct {
	serving.RuntimeObservation
	started time.Time
}

func (o recoveryBootObservation) Recovery() serving.ObservationRecovery {
	return serving.ObservationRecovery{Unknown: true, Started: o.started}
}

// Reuse the real recorder and source/operation adapters with a boot that has a
// lost tail. No HTTP request is reconstructed from the missing evidence.
func recoveryBootRuntime(t *testing.T, c *SchedulerController) {
	t.Helper()
	definitions := c.runtime.SchedulerInputs(time.Now).Workloads
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rt, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 8,
		ObservationLog: recoveryBootObservation{RuntimeObservation: c.observationLog, started: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if err := rt.Publish(definition); err != nil {
			t.Fatal(err)
		}
	}
	c.runtime, c.observer.runtime, c.operations.Runtime = rt, rt, rt
	rt.EnableSchedulerRouting()
}

func recoveryPeerScenario(t *testing.T, condition string) (*SchedulerController, *atomic.Bool) {
	t.Helper()
	recovering := &atomic.Bool{}
	recovering.Store(true)
	c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
		if index != 1 || !recovering.Load() {
			return
		}
		switch condition {
		case "busy":
			state.Requests = []native.RequestState{{Phase: "running"}}
		case "failed read with stale empty state":
			state.At = time.Time{}
		case "metadata mismatch":
			state.Metadata.ProfileID = "not-the-measured-profile"
		}
	})
	prepared.Store(true)
	recoveryBootRuntime(t, c)
	// A failed read must not recycle this previous boot's empty actor.
	for _, profile := range c.observer.Profiles() {
		if profile.NodeName == "b" {
			c.observer.sources["b-pod"].instance = serving.ReadyInstance{Profile: &profile,
				State: serving.InstanceState{ID: "b-pod/containerd://running", PodUID: "b-pod",
					Node: serving.NodeReference{Name: "b", UID: "b-node"}, Profile: profile.ID,
					ObservedAt: c.runtime.Recovery().Started.Add(-time.Second), Native: &native.State{Ready: true}}}
		}
	}
	kube := c.kube.(*kubefake.Clientset)
	bPod, err := kube.CoreV1().Pods("chill-system").Get(context.Background(), "b", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		list := action.(ktesting.ListAction)
		if !recovering.Load() || list.GetListRestrictions().Labels.String() != servingstate.InstancePodUIDLabel+"=b-pod" ||
			condition != "ambiguous Service" && condition != "changed container" {
			return false, nil, nil
		}
		pod := bPod.DeepCopy()
		result := &core.PodList{Items: []core.Pod{*pod}}
		if condition == "ambiguous Service" {
			duplicate := pod.DeepCopy()
			duplicate.Name, duplicate.UID = "duplicate-native-source", types.UID("ambiguous-pod")
			result.Items = append(result.Items, *duplicate)
		} else {
			result.Items[0].Status.ContainerStatuses[0].ContainerID = "containerd://changed-during-recovery"
		}
		return true, result, nil
	})
	return c, recovering
}

func TestRecoveryFreshExecutorRoutesWhileUnconfirmedPeerRetainsPhysicalHold(t *testing.T) {
	for _, condition := range []string{"busy", "failed read with stale empty state", "metadata mismatch", "ambiguous Service", "changed container"} {
		t.Run(condition, func(t *testing.T) {
			c, recovering := recoveryPeerScenario(t, condition)
			ctx := context.Background()
			if err := c.observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			a := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
			b := schedulerPhysicalState("b-pod/containerd://running", c.runtime.Instances())
			if a == nil || !a.Ready || a.RecoveryUnknown || b == nil || !b.RecoveryUnknown || b.Ready {
				t.Fatalf("fresh A did not recover independently of B: A=%+v B=%+v", a, b)
			}
			uid := c.runtime.SchedulerInputs(time.Now).Workloads[0].UID
			var profile *serving.Profile
			for _, p := range c.observer.Profiles() {
				if p.ID == a.Profile {
					copy := p
					profile = &copy
				}
			}
			instance := serving.SchedulerInstance{ID: a.ID, PodUID: a.PodUID, Node: a.Node, Profile: profile,
				WorkloadUID: uid, Allocation: a.Allocation, Ready: true, Weight: 1, MaxBatch: 1, Capacity: 4}
			target := serving.SchedulerCandidate{ID: "fresh-executor-route", Instances: []serving.SchedulerInstance{instance},
				Routing: map[string]map[string]int32{uid: {a.ID: 1000}}}
			result := &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRoute, WorkloadUID: uid}}
			if err := c.operations.ApplyTarget(ctx, target, result); err != nil {
				t.Fatal("healthy exact executor could not accept a route", err)
			}
			routing, _ := c.runtime.SchedulerRouting()
			if routing[uid][a.ID] <= 0 || routing[uid][b.ID] != 0 {
				t.Fatalf("healthy route was globally fenced or unknown B was routed: %v", routing)
			}
			var operations []servingstate.Operation
			for _, kind := range []string{servingstate.OperationPower, servingstate.OperationPrepare, servingstate.OperationResize, servingstate.OperationRemove, servingstate.OperationStop, servingstate.OperationDrain} {
				operation := servingstate.Operation{Kind: kind, Node: b.Node, Instance: b.ID, PodUID: b.PodUID}
				operation.Prepare = &servingstate.PrepareSpec{Instance: serving.SchedulerInstance{ID: "replacement", Node: b.Node}}
				operation.Target = &serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{operation.Prepare.Instance}}
				operations = append(operations, operation)
				if err := c.operations.ValidateOperationBinding(ctx, operation); err == nil {
					t.Fatalf("%s authorized unknown physical executor", kind)
				}
			}
			if err := c.operations.RequestPowerMode(ctx, b.Node, "", "fixed", time.Second); err == nil {
				t.Fatal("direct power operation bypassed unknown occupancy")
			}
			if err := c.operations.StopInstance(ctx, b.ID); err == nil {
				t.Fatal("direct stop bypassed unknown executor")
			}
			if err := c.operations.ApplyPolicy(ctx, b.ID, native.Policy{}, result); err == nil {
				t.Fatal("direct native policy mutation bypassed unknown executor")
			}
			if _, err := c.operations.MeasurementIsolation(ctx, b.Node, nil, nil, 0); err == nil {
				t.Fatal("measurement certified isolation over unknown physical occupancy")
			}
			recovering.Store(false)
			if err := c.observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			b = schedulerPhysicalState(b.ID, c.runtime.Instances())
			if b == nil || b.RecoveryUnknown || !b.Ready {
				t.Fatalf("fresh verified empty B did not recover: %+v", b)
			}
			for _, operation := range operations {
				if err := c.operations.ValidateOperationBinding(ctx, operation); err != nil {
					t.Fatalf("%s recovery fence survived fresh exact proof: %v", operation.Kind, err)
				}
			}
		})
	}
}

func TestRecoveryLatePendingPodKeepsAllocationWithoutFencingHealthyRoute(t *testing.T) {
	c, prepared := tickReadySubsetController(t, nil)
	prepared.Store(true)
	recoveryBootRuntime(t, c)
	ctx := context.Background()
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	pod, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "a", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pending := pod.DeepCopy()
	pending.Name, pending.UID = "late-pending", "late-pending-pod"
	pending.Labels[servingstate.InstancePodUIDLabel] = string(pending.UID)
	pending.Annotations[servingstate.ServingRoutingAnnotation] = "{}"
	pending.Status = core.PodStatus{Phase: core.PodPending}
	if _, err := c.kube.CoreV1().Pods(pending.Namespace).Create(ctx, pending, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(snapshot.Instances, func(i serving.SchedulerInstance) bool { return i.PodUID == string(pending.UID) })
	if index < 0 || snapshot.Instances[index].Ready || snapshot.Instances[index].Allocation.MemoryBytes == 0 {
		t.Fatalf("late pending source became free capacity: %+v", snapshot.Instances)
	}
	a := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
	if a == nil || !a.Ready || a.RecoveryUnknown {
		t.Fatalf("pending peer fenced healthy inference destination: %+v", a)
	}
	if err := c.operations.RequestPowerMode(ctx, a.Node, "", "fixed", time.Second); err == nil {
		t.Fatal("healthy co-resident authorized node-wide power over pending unknown peer")
	}
	if err := c.operations.ClearNode(ctx, a.Node); err == nil {
		t.Fatal("node cleanup discarded pending unknown peer")
	}
}

func TestRecoveryPhysicalReleaseRequiresExactTerminatedContainer(t *testing.T) {
	c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
		if index == 1 {
			state.Requests = []native.RequestState{{Phase: "running"}}
		}
	})
	prepared.Store(true)
	recoveryBootRuntime(t, c)
	ctx := context.Background()
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	pod, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "b", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	operation := servingstate.Operation{Kind: servingstate.OperationPower, Node: serving.NodeReference{Name: "b", UID: "b-node"}}
	// A last termination for another container cannot certify this executor.
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated = &core.ContainerStateTerminated{ContainerID: "containerd://other"}
	if _, err := c.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.operations.ValidateOperationBinding(ctx, operation); err == nil {
		t.Fatal("another container's termination freed the busy executor's Node")
	}
	pod.Status.Phase = core.PodFailed
	pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ContainerID: "containerd://running"}}
	if _, err := c.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.operations.ValidateOperationBinding(ctx, operation); err != nil {
		t.Fatal("exact terminated executor retained its inference recovery hold", err)
	}
}
