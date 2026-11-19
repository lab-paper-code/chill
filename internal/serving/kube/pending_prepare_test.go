package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

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
		// An ordinary delivery wait must exhaust the preparation deadline;
		// observed pull failures end preparation and are separately retryable.
		pod.Status.ContainerStatuses = []core.ContainerStatus{{Name: servingstate.FirstfloorContainerName, State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "ContainerCreating"}}}}
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
			return servingstate.IgnoreNotFound(c.kube.Tracker().Delete(core.SchemeGroupVersion.WithResource("pods"), pod.Namespace, pod.Name))
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

// A prepare step that ends while its Pod is still Pending hands the Pod back:
// the finalizer survives the DELETE request, and the Node taint and the
// reservation stay until the kubelet writes the terminal state.
func TestOperationCostPrepareTimeoutHandsBackPendingPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o := opcostHarnessOn(t, newAdmissionHarness(t), nil)
	c := o.controller
	bridge := &pendingPreparationAPI{Client: c.store.client, kube: o.kube, deleted: make(chan *core.Pod, 1)}
	c.store.client = bridge
	c.measureStep = func(ctx context.Context, _ SchedulerControllerConfig, request servingops.MeasurementRequest) (servingops.OperationMeasurement, evidencestore.Receipt, error) {
		prepare, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		_, err := c.measurement.PrepareInstance(prepare, *request.Operation.Prepare)
		return servingops.OperationMeasurement{}, evidencestore.Receipt{}, err
	}
	completed := make(chan servingstate.OperationResult, 1)
	c.mu.Lock()
	for id, spec := range c.config.Preparations {
		spec.Config.Policy.ProfileID = id
		c.config.Preparations[id] = spec
	}
	c.config.OnAction = func(result servingstate.OperationResult) error { completed <- result; return nil }
	c.mu.Unlock()
	m := opcostRoot("pending-timeout", time.Now(), edge.OperationCostStep{Name: "prepare", Kind: edge.OperationCostStepPrepare, BatchEnergyOptimization: "beo"})
	o.create(m)
	if err := c.Admit(ctx); err != nil {
		t.Fatal(err)
	}
	var deleting *core.Pod
	select {
	case deleting = <-bridge.deleted:
	case result := <-completed:
		t.Fatalf("measurement ended before retained DELETE: %+v", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if deleting.DeletionTimestamp == nil || len(deleting.Finalizers) != 1 || deleting.Finalizers[0] != servingstate.SourceFinalizer || servingstate.PodTerminated(deleting) || !servingstate.PodAwaitingExecutor(deleting) {
		t.Fatalf("pending deletion did not retain termination proof: %+v", deleting)
	}
	if o.holder() != string(m.UID) {
		t.Fatal("hand-back released taint before terminal status")
	}
	if err := c.authorize(servingstate.Operation{Node: testNode}); err == nil {
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
	var result servingstate.OperationResult
	select {
	case result = <-completed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	c.wg.Wait()
	status := o.status(m)
	if status.Phase != edge.MeasurementPhaseFailed || !strings.Contains(status.Reason, context.DeadlineExceeded.Error()) {
		t.Fatalf("prepare timeout was not preserved: %+v", status)
	}
	if !strings.Contains(strings.Join(result.Progress, ";"), "taint removed") || o.holder() != "" {
		t.Fatalf("hand-back incomplete: %+v, holder %q", result, o.holder())
	}
	if err := c.store.client.Get(ctx, client.ObjectKeyFromObject(deleting), &core.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("terminated preparation Pod retained: %v", err)
	}
	pods, err := o.kube.CoreV1().Pods(c.observer.config.Namespace).List(ctx, meta.ListOptions{})
	if err != nil || len(pods.Items) != 0 {
		t.Fatalf("native Pods remain: %v, %v", pods, err)
	}
	if err := c.authorize(servingstate.Operation{Node: testNode}); err != nil {
		t.Fatal("hand-back retained reservation", err)
	}
	c.observer.mu.Lock()
	source = c.observer.sources[string(deleting.UID)]
	c.observer.mu.Unlock()
	if source != nil || len(c.runtime.Instances()) != 0 {
		t.Fatal("completed hand-back retained physical executor")
	}
}
