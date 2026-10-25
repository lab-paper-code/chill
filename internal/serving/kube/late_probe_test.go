package kube

import (
	"context"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestLateProbeCannotResurrectRemovedExecutor(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "same-name-new-uid"}[replacement], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			api := newObserverAPI(t, 1)
			o := api.observer
			api.owner = "workload-uid"
			api.logOpened = make(chan struct{}, 2)
			api.refresh(t)
			select {
			case <-api.logOpened:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			pod, node, _, _ := observerSourceFixture()
			pod.Name, pod.UID = "instance-a", "pod-uid-a"
			pod.Labels[instancePodUIDLabel] = string(pod.UID)
			pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", Name: "w", UID: api.owner}}
			c := o.store.client.(*managerAPI).Client
			if err := c.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			api.mu.Lock()
			clear(api.reads)
			api.verifiedList, api.releaseList = make(chan struct{}), make(chan struct{})
			ready, release := api.verifiedList, api.releaseList
			beforeLogs := api.logFollows
			api.mu.Unlock()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			refreshed := make(chan error, 1)
			go func() { refreshed <- o.Refresh(ctx) }()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// The HTTP probe and its running-Pod LIST have completed, but publication
			// waits until normal removal has observed exact termination and retired UID.
			pod.Status.Phase = core.PodSucceeded
			status := &pod.Status.ContainerStatuses[0]
			status.Ready = false
			status.State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ContainerID: status.ContainerID}}
			if err := c.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
				t.Fatal(err)
			}
			kube := kubefake.NewSimpleClientset(node, pod, observerServiceFixture(pod))
			kube.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
				current := &core.Pod{}
				err := c.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: action.(ktesting.GetAction).GetName()}, current)
				return true, current, err
			})
			operations := &Operations{store: o.store, kube: kube, runtime: o.runtime, observer: o, native: o.native,
				config: OperationConfig{Namespace: pod.Namespace, PollInterval: time.Millisecond}}
			id := physicalID(pod.UID, status.ContainerID)
			if err := operations.RemoveInstance(ctx, serving.NodeReference{Name: node.Name, UID: string(node.UID)}, id); err != nil {
				t.Fatal(err)
			}
			var next *nativeSource
			if replacement {
				fresh := pod.DeepCopy()
				fresh.UID, fresh.ResourceVersion, fresh.DeletionTimestamp = "late-probe-replacement-uid", "", nil
				fresh.Finalizers = nil
				fresh.OwnerReferences = nil
				fresh.Labels[instancePodUIDLabel] = string(fresh.UID)
				fresh.Status.ContainerStatuses[0].State = core.ContainerState{Running: &core.ContainerStateRunning{}}
				fresh.Status.ContainerStatuses[0].Ready = true
				if err := c.Create(ctx, fresh); err != nil {
					t.Fatal(err)
				}
				o.mu.Lock()
				o.restoreSources([]core.Pod{*fresh})
				next = o.sources[string(fresh.UID)]
				next.instance.State.Reason = "replacement-not-probed"
				o.mu.Unlock()
			}
			close(release)
			if err := <-refreshed; err != nil {
				t.Fatal(err)
			}
			o.mu.Lock()
			old := o.sources[string(pod.UID)]
			changed := next != nil && (next.cancel != nil || next.instance.State.Ready || next.instance.State.Reason != "replacement-not-probed")
			o.mu.Unlock()
			if old != nil || changed {
				t.Fatal("late probe resurrected old source or acted for replacement")
			}
			for _, instance := range o.runtime.Instances() {
				if instance.ID == id {
					t.Fatal("retired executor reappeared in runtime")
				}
			}
			if done, err := o.CleanupWorkload(ctx, api.owner); err != nil || !done {
				t.Fatalf("cleanup = %v, %v", done, err)
			}
			api.mu.Lock()
			afterLogs := api.logFollows
			api.mu.Unlock()
			if afterLogs != beforeLogs {
				t.Fatalf("late probe restarted log follow: %d -> %d", beforeLogs, afterLogs)
			}
		})
	}
}

func TestClearNodeCannotStripUnobservedRunningSourceFinalizer(t *testing.T) {
	ctx := context.Background()
	pod, node, _, _ := observerSourceFixture()
	pod.Finalizers = []string{sourceFinalizer}
	o := newTestObserver(t, pod)
	kube := kubefake.NewSimpleClientset(node, pod, observerServiceFixture(pod))
	operations := &Operations{store: o.store, kube: kube, runtime: o.runtime, observer: o,
		config: OperationConfig{Namespace: pod.Namespace, PollInterval: time.Millisecond}}
	if err := operations.clearNode(ctx, serving.NodeReference{Name: node.Name, UID: string(node.UID)}); err == nil {
		t.Fatal("unobserved running executor was cleared without termination proof")
	}
	current, err := kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
	if err != nil || len(current.Finalizers) != 1 || current.Finalizers[0] != sourceFinalizer || current.DeletionTimestamp != nil {
		t.Fatalf("clearNode bypassed retained termination evidence: %v, %v", current, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "patch" || action.GetVerb() == "delete" {
			t.Fatalf("unproven executor was mutated: %v", action)
		}
	}
}

func TestDeletionReservesLiveOwnedResourcesOutsideSnapshot(t *testing.T) {
	ctx := context.Background()
	pod, node, _, _ := observerSourceFixture()
	uid := types.UID("workload-uid")
	pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: uid}}
	o := newTestObserver(t, pod)
	c := &SchedulerController{store: o.store, observer: o, kube: kubefake.NewSimpleClientset(node), runs: map[string]*actionRun{}}
	c.mu.Lock()
	held := c.reserveRun("physical-only", &actionRun{action: serving.Action{Status: nativeOperationRunning}}, Operation{PodUID: string(pod.UID)})
	c.mu.Unlock()
	if !held {
		t.Fatal("failed to reserve Pod")
	}
	if err := c.reserveDeletion(ctx, uid); err == nil {
		t.Fatal("deletion ignored live Pod absent from snapshot")
	}
	c.mu.Lock()
	delete(c.runs, "physical-only")
	c.mu.Unlock()
	if err := c.reserveDeletion(ctx, uid); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []Operation{
		{WorkloadUID: string(uid)},
		{PodUID: string(pod.UID)},
		{Instance: physicalID(pod.UID, firstfloorStatus(pod).ContainerID)},
		{Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}},
	} {
		if err := c.authorize(operation); err == nil {
			t.Fatalf("deletion did not reserve %+v", operation)
		}
	}
	// Scope remains idempotently owned while cleanup is still pending.
	if err := c.reserveDeletion(ctx, uid); err != nil {
		t.Fatal(err)
	}
	c.releaseDeletion(uid)
	if err := c.authorize(Operation{PodUID: string(pod.UID)}); err != nil {
		t.Fatal(err)
	}
}
