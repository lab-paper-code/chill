package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// pendingPreparationAPI shares the writer's Pods with the native clientset.
// DELETE retains the Pod until the test supplies kubelet terminal status.
type pendingPreparationAPI struct {
	client.Client
	kube    *kubefake.Clientset
	deleted chan *core.Pod
}

func (c *pendingPreparationAPI) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if pod, ok := object.(*core.Pod); ok {
		pod.Status.Phase = core.PodPending
		pod.Status.ContainerStatuses = []core.ContainerStatus{{Name: firstfloorContainerName, State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}
	}
	if err := c.Client.Create(ctx, object, opts...); err != nil {
		return err
	}
	if pod, ok := object.(*core.Pod); ok {
		return c.kube.Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace)
	}
	return nil
}

func (c *pendingPreparationAPI) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.Client.Patch(ctx, object, patch, opts...); err != nil {
		return err
	}
	if pod, ok := object.(*core.Pod); ok {
		current := &core.Pod{}
		err := c.Client.Get(ctx, client.ObjectKeyFromObject(pod), current)
		if apierrors.IsNotFound(err) {
			return eIfNotFound(c.kube.Tracker().Delete(core.SchemeGroupVersion.WithResource("pods"), pod.Namespace, pod.Name))
		}
		if err != nil {
			return err
		}
		return c.kube.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), current, pod.Namespace)
	}
	return nil
}

func (c *pendingPreparationAPI) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	if err := c.Client.Delete(ctx, object, opts...); err != nil {
		return err
	}
	if pod, ok := object.(*core.Pod); ok {
		current := &core.Pod{}
		if err := c.Client.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
			return err
		}
		select {
		case c.deleted <- current:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestOccupancyPreparationTimeoutHandsBackPendingPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := newAdmissionHarness(t)
	c := h.controller
	_, _, _, class := observerSourceFixture()
	workload := &edge.Workload{ObjectMeta: meta.ObjectMeta{Name: "pending-owner"}}
	measurement := occupancyRoot("pending-timeout", time.Now(), 1)
	measurement.Spec.PreparationSeconds = 1
	h.create(&class, workload, measurement)
	spec := measuredCPUPreparationFixture()
	profile := spec.Instance.Profile
	c.observer.SetProfiles([]serving.Profile{*profile})
	bridge := &pendingPreparationAPI{Client: c.store.client, kube: h.kube, deleted: make(chan *core.Pod, 1)}
	c.store.client = bridge
	h.setNode(func(node *core.Node) {
		node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: string(measurement.UID), Effect: core.TaintEffectNoSchedule}}
	})
	completed := make(chan OperationResult, 1)
	c.mu.Lock()
	c.config.Preparations = map[string]PrepareSpec{profile.ID: spec}
	c.config.OnAction = func(result OperationResult) error { completed <- result; return nil }
	config := c.config
	c.mu.Unlock()
	c.startOccupancy(h.node, measurement, occupancyPlan{profile: profile, workload: workload, powered: true}, config)
	var deleting *core.Pod
	select {
	case deleting = <-bridge.deleted:
	case result := <-completed:
		t.Fatalf("occupancy ended before retained DELETE: %+v", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if deleting.DeletionTimestamp == nil || len(deleting.Finalizers) != 1 || deleting.Finalizers[0] != sourceFinalizer || podTerminated(deleting) || !podAwaitingExecutor(deleting) {
		t.Fatalf("pending deletion did not retain termination proof: %+v", deleting)
	}
	if h.holder() != string(measurement.UID) {
		t.Fatal("hand-back released taint before terminal status")
	}
	if err := c.authorize(Operation{Node: testNode}); err == nil {
		t.Fatal("hand-back released reservation before terminal status")
	}
	c.observer.mu.Lock()
	source := c.observer.sources[string(deleting.UID)]
	stopped := source == nil || source.stopped
	c.observer.mu.Unlock()
	if stopped {
		t.Fatal("DELETE or absent container ID was treated as termination proof")
	}
	// Only kubelet's terminal status permits finalizer release and retirement.
	deleting.Status.Phase = core.PodFailed
	if err := bridge.Client.Status().Update(ctx, deleting); err != nil {
		t.Fatal(err)
	}
	var result OperationResult
	select {
	case result = <-completed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	c.wg.Wait()
	if result.Status != nativeOperationFailed || !strings.Contains(result.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("prepare timeout was not preserved: %+v", result)
	}
	if !strings.Contains(strings.Join(result.Progress, ";"), "taint removed") || h.holder() != "" {
		t.Fatalf("hand-back incomplete: %+v, holder %q", result, h.holder())
	}
	if err := c.store.client.Get(ctx, client.ObjectKeyFromObject(deleting), &core.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("terminated preparation Pod retained: %v", err)
	}
	pods, err := h.kube.CoreV1().Pods(c.observer.config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil || len(pods.Items) != 0 {
		t.Fatalf("native Pods remain: %v, %v", pods, err)
	}
	if err := c.authorize(Operation{Node: testNode, WorkloadUID: string(workload.UID)}); err != nil {
		t.Fatal("hand-back retained reservation", err)
	}
	c.observer.mu.Lock()
	source = c.observer.sources[string(deleting.UID)]
	c.observer.mu.Unlock()
	if source != nil || len(c.runtime.Instances()) != 0 {
		t.Fatal("completed hand-back retained physical executor")
	}
}
