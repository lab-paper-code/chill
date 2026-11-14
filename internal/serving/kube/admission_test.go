package kube

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// nodeAPI serves the Nodes of the store client from the clientset that holds
// them. agent, when set, plays the actuation agent on every patched Node.
type nodeAPI struct {
	client.Client
	kube  kubernetes.Interface
	agent func(*core.Node)
}

func (c *nodeAPI) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	node, ok := obj.(*core.Node)
	if !ok {
		return c.Client.Get(ctx, key, obj, opts...)
	}
	current, err := c.kube.CoreV1().Nodes().Get(ctx, key.Name, meta.GetOptions{})
	if err == nil {
		current.DeepCopyInto(node)
	}
	return err
}

func (c *nodeAPI) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	node, ok := obj.(*core.Node)
	if !ok {
		return c.Client.Patch(ctx, obj, patch, opts...)
	}
	raw, err := patch.Data(obj)
	if err != nil {
		return err
	}
	patched, err := c.kube.CoreV1().Nodes().Patch(ctx, node.Name, patch.Type(), raw, meta.PatchOptions{})
	if err == nil && c.agent != nil {
		c.agent(patched)
		patched, err = c.kube.CoreV1().Nodes().Update(ctx, patched, meta.UpdateOptions{})
	}
	if err == nil {
		patched.DeepCopyInto(node)
	}
	return err
}

// settleAgent applies each requested mode of class at once, as the actuation
// agent does: read-back, generation + 1, transition record and success.
func settleAgent(class *edge.DeviceClass) func(*core.Node) {
	return func(n *core.Node) {
		mode := n.Labels[chillmeta.PowerMode]
		i := slices.IndexFunc(class.Spec.PowerModes, func(m edge.PowerMode) bool { return m.Name == mode })
		if i < 0 {
			return
		}
		generation, _, _ := chillmeta.NodePowerModeGeneration(n)
		now := time.Now()
		actuator, _ := json.Marshal(class.Spec.PowerModes[i].Actuator)
		chillmeta.SetPowerModeTransition(n, chillmeta.PowerModeSuccess, generation+1, chillmeta.PowerModeTransitionRecord{
			PowerMode: mode, RequestedAt: n.Annotations[chillmeta.PowerModeRequestedAt], Generation: generation + 1, ApplyStartedAt: &now, VerifiedAt: &now, ReadyAt: &now})
		n.Annotations[chillmeta.PowerActuator] = string(actuator)
	}
}

// pendingNode marks the Node as applying mode requested at requested.
func pendingNode(n *core.Node, mode string, requested time.Time) {
	generation, _, _ := chillmeta.NodePowerModeGeneration(n)
	chillmeta.SetPowerModeTransition(n, chillmeta.PowerModePending, generation, chillmeta.PowerModeTransitionRecord{PowerMode: mode, RequestedAt: requested.Format(time.RFC3339Nano)})
}

type admissionHarness struct {
	t          *testing.T
	controller *SchedulerController
	kube       *kubefake.Clientset
	node       *core.Node
	nodes      *nodeAPI
}

func newAdmissionHarness(t *testing.T, kubeObjects ...client.Object) *admissionHarness {
	t.Helper()
	_, node, _, _ := observerSourceFixture()
	objects := []runtime.Object{node}
	for _, o := range kubeObjects {
		objects = append(objects, o)
	}
	kube := kubefake.NewSimpleClientset(objects...)
	controller, nodes := newAdmissionController(t, kube)
	return &admissionHarness{t: t, controller: controller, kube: kube, node: node, nodes: nodes}
}

// newAdmissionController builds a scheduler controller on the fake store and
// the given Kubernetes client, whose Nodes the returned store client serves.
func newAdmissionController(t *testing.T, kube kubernetes.Interface) (*SchedulerController, *nodeAPI) {
	t.Helper()
	manager, _ := newManagerHarness(t)
	manager.observer.kube = kube
	nodes := &nodeAPI{Client: manager.store.client, kube: kube}
	manager.store.client = nodes
	operations, err := servingops.NewOperations(manager.reservations, manager.store, kube, manager.runtime, manager.observer, manager.observer.native,
		servingops.OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewSchedulerController(manager.reservations, manager.store, kube, manager.runtime, manager.observer, operations, manager.observationLog,
		SchedulerControllerConfig{
			Settings: withSchedulerEstimateFixture(serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second, MaxReplicas: 1, MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1,
				CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
				PlanSearchStrategy: "v1", PlanSearchEvaluations: 500000, PlanExactFinalists: 16, PlanPolishActive: 4,
				CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1}),
			MeasurementNamespaces: []string{"chill-system"},
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	return controller, nodes
}

func (h *admissionHarness) create(objects ...client.Object) {
	h.t.Helper()
	for _, o := range objects {
		if err := h.controller.store.client.Create(context.Background(), o); err != nil {
			h.t.Fatal(err)
		}
	}
}

// setNode changes the harness Node in the API.
func (h *admissionHarness) setNode(change func(*core.Node)) {
	h.t.Helper()
	node, err := h.kube.CoreV1().Nodes().Get(context.Background(), h.node.Name, meta.GetOptions{})
	if err != nil {
		h.t.Fatal(err)
	}
	change(node)
	if _, err = h.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
		h.t.Fatal(err)
	}
}

// admit runs one cycle and waits for the runs it started.
func (h *admissionHarness) admit() {
	h.t.Helper()
	if err := h.controller.Admit(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.controller.mu.Lock()
		running := false
		for _, run := range h.controller.runs {
			running = running || run.action.Kind != measurementLeaseKind && run.action.Status == servingstate.NativeOperationRunning
		}
		h.controller.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			h.t.Fatal("run did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *admissionHarness) holder() string {
	h.t.Helper()
	node, err := h.kube.CoreV1().Nodes().Get(context.Background(), h.node.Name, meta.GetOptions{})
	if err != nil {
		h.t.Fatal(err)
	}
	value, _ := profilingrun.MeasurementHolder(node)
	return value
}

// admission reads the admission annotation of object.
func (h *admissionHarness) admission(object client.Object) string {
	h.t.Helper()
	if err := h.controller.store.client.Get(context.Background(), client.ObjectKeyFromObject(object), object); err != nil {
		h.t.Fatal(err)
	}
	return object.GetAnnotations()[profilingrun.AdmissionAnnotation]
}

// complete makes the measurement terminal.
func (h *admissionHarness) complete(root *edge.ModelLoadedIdleBaseline) {
	h.t.Helper()
	if err := h.controller.store.client.Get(context.Background(), client.ObjectKeyFromObject(root), root); err != nil {
		h.t.Fatal(err)
	}
	root.Status.Phase = edge.ModelLoadedIdleBaselinePhaseComplete
	if err := h.controller.store.client.Update(context.Background(), root); err != nil {
		h.t.Fatal(err)
	}
}

func baselineRoot(name string, created time.Time) *edge.ModelLoadedIdleBaseline {
	o := &edge.ModelLoadedIdleBaseline{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: name, UID: types.UID(name + "-uid"), CreationTimestamp: meta.NewTime(created)}}
	o.Spec.State.TargetNode = testNode.Name
	return o
}

func servingPod(name string) *core.Pod {
	return &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: name, UID: types.UID(name + "-uid"),
		Labels: map[string]string{"app.kubernetes.io/component": servingstate.ServingInstanceComponent}}, Spec: core.PodSpec{NodeName: testNode.Name}}
}

func TestAdmitGrantsEmptyNodeAndIgnoresPodsCHILLDidNotPlace(t *testing.T) {
	external := &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: "default", Name: "plan10-serving-jetsono"}, Spec: core.PodSpec{NodeName: testNode.Name}}
	h := newAdmissionHarness(t, external)
	root := baselineRoot("idle", time.Now())
	h.create(root)
	h.admit()
	if h.holder() != string(root.UID) {
		t.Fatalf("taint holder = %q", h.holder())
	}
	if granted, _, _ := profilingrun.Admission(map[string]string{profilingrun.AdmissionAnnotation: h.admission(root)}); !granted {
		t.Fatal("measurement not granted")
	}
	if _, err := h.kube.CoreV1().Pods("default").Get(context.Background(), external.Name, meta.GetOptions{}); err != nil {
		t.Fatalf("external Pod touched: %v", err)
	}
}

func TestAdmitRefusesNodeServingAndNamesThePod(t *testing.T) {
	h := newAdmissionHarness(t, servingPod("serving-a"))
	root := baselineRoot("idle", time.Now())
	h.create(root)
	h.admit()
	if got := h.admission(root); got != "Refused: NodeServing chill-system/serving-a" {
		t.Fatalf("admission = %q", got)
	}
	if h.holder() != "" {
		t.Fatal("serving Node tainted")
	}
}

func TestAdmitRefusesTerminalServingPodUntilDeleted(t *testing.T) {
	for _, phase := range []core.PodPhase{core.PodFailed, core.PodSucceeded} {
		t.Run(string(phase), func(t *testing.T) {
			pod := servingPod("terminal-serving")
			pod.Status.Phase = phase
			h := newAdmissionHarness(t, pod)
			root := baselineRoot("before-delete", time.Now())
			h.create(root)
			h.admit()
			if got := h.admission(root); got != "Refused: NodeServing chill-system/terminal-serving" || h.holder() != "" {
				t.Fatalf("terminal Pod was treated as absence: admission=%q holder=%q", got, h.holder())
			}
			if err := h.kube.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, meta.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			after := baselineRoot("after-delete", time.Now())
			h.create(after)
			h.admit()
			if granted, _, _ := profilingrun.Admission(map[string]string{profilingrun.AdmissionAnnotation: h.admission(after)}); !granted {
				t.Fatal("actual Pod absence did not allow measurement")
			}
		})
	}
}

func TestAdmitRefusesMissingNode(t *testing.T) {
	h := newAdmissionHarness(t)
	root := baselineRoot("idle", time.Now())
	root.Spec.State.TargetNode = "gone"
	h.create(root)
	h.admit()
	if got := h.admission(root); got != "Refused: NodeMissing gone" {
		t.Fatalf("admission = %q", got)
	}
}

func TestAdmitQueuesBehindTheHolderAndItsServingPods(t *testing.T) {
	h := newAdmissionHarness(t)
	first, second := baselineRoot("first", time.Now().Add(-time.Minute)), baselineRoot("second", time.Now())
	h.create(second, first)
	h.admit()
	if h.holder() != string(first.UID) || h.admission(second) != "" {
		t.Fatalf("holder %q, second %q", h.holder(), h.admission(second))
	}
	// The holder's own serving Pod does not refuse the queued measurement.
	if _, err := h.kube.CoreV1().Pods("chill-system").Create(context.Background(), servingPod("runner"), meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.admit()
	if h.admission(second) != "" {
		t.Fatal("queued measurement refused while the Node is held")
	}
	h.complete(first)
	h.admit() // No status is not termination proof, even for the old holder.
	if pods, _ := h.kube.CoreV1().Pods("chill-system").List(context.Background(), meta.ListOptions{}); len(pods.Items) != 1 {
		t.Fatalf("hand-back discarded unconfirmed serving Pod: %d", len(pods.Items))
	}
	if h.holder() != string(first.UID) {
		t.Fatal("hand-back released the Node before termination proof")
	}
	// The holder's worker has now actually removed its Pod; the next hand-back
	// observes the empty Node rather than stripping an unknown source finalizer.
	if err := h.kube.CoreV1().Pods("chill-system").Delete(context.Background(), "runner", meta.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	h.admit()
	if h.holder() != "" {
		t.Fatalf("taint kept by %q", h.holder())
	}
	h.admit()
	if h.holder() != string(second.UID) {
		t.Fatalf("queued measurement not granted after hand-back: %q", h.holder())
	}
}

func TestAdmitWaitsForRunningRunAndPendingPowerChange(t *testing.T) {
	h := newAdmissionHarness(t)
	h.create(cpuClass())
	h.controller.mu.Lock()
	h.controller.config.PowerPolicies = []nodeactuator.ActuationPolicySpec{{Name: "low", DeviceClass: "cpu", PowerMode: "low", ActuatorKind: string(edge.PowerModeActuatorJetsonNVPModel), MaxActuationDurationSeconds: 60}}
	h.controller.mu.Unlock()
	root := baselineRoot("idle", time.Now())
	h.create(root)
	release := make(chan struct{})
	h.controller.startNodeRun(servingstate.Operation{ID: "prepare", Kind: servingstate.OperationPrepare, Node: serving.NodeReference{Name: "node", UID: "node-uid"}}, SchedulerControllerConfig{},
		func(context.Context, *servingstate.OperationResult) error { <-release; return nil })
	if err := h.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.holder() != "" {
		t.Fatal("granted a Node with a running run")
	}
	close(release)
	h.setNode(func(n *core.Node) { pendingNode(n, "low", time.Now()) })
	h.admit()
	if h.holder() != "" {
		t.Fatal("granted a Node with a pending power change")
	}
	// Past its fence plus maxActuationDurationSeconds a pending mode no longer holds the Node.
	h.setNode(func(n *core.Node) { pendingNode(n, "low", time.Now().Add(-time.Minute)) })
	h.admit()
	if h.holder() != string(root.UID) {
		t.Fatal("not granted after the power change deadline")
	}
}

func TestHandBackReturnsNodeOfVanishedMeasurement(t *testing.T) {
	for _, holder := range []string{"true", "deleted-uid"} {
		h := newAdmissionHarness(t)
		node := h.node.DeepCopy()
		node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: holder, Effect: core.TaintEffectNoSchedule}}
		if _, err := h.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		h.admit()
		state := h.controller.State()
		if h.holder() != "" || len(state.Results) != 1 || !strings.Contains(strings.Join(state.Results[0].Progress, ";"), "Interrupted "+holder) {
			t.Fatalf("holder %q not handed back: %+v", holder, state.Results)
		}
	}
}

func TestAdmitKeepsLiveGrantAcrossRestart(t *testing.T) {
	h := newAdmissionHarness(t)
	root := baselineRoot("idle", time.Now())
	h.create(root)
	h.admit()
	restarted := newAdmissionHarness(t)
	restarted.kube, restarted.controller.kube = h.kube, h.kube
	restarted.controller.store = h.controller.store
	restarted.admit()
	if h.holder() != string(root.UID) || len(restarted.controller.State().Results) != 0 {
		t.Fatal("live measurement handed back after restart")
	}
}

// A failed session can leave a live profiling executor behind when ChillServer
// restarts. Fresh observer proof releases recovery, then admission retries the
// normal hand-back without changing the terminal session or needing an operator.
func TestHandBackFailedSessionAfterRestartWithMeasurementInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture, node, profile, class := observerSourceFixture()
	profile.Condition.Preprocessor.Inputs, profile.Condition.Preprocessor.Outputs = []string{"input"}, []string{"output"}
	profile.RuntimeMetadata.RuntimeVersion = observerTestRuntime
	profile.RuntimeMetadata.Inputs, profile.RuntimeMetadata.Outputs = []string{"input"}, []string{"output"}
	profile.RuntimeMetadata.InitializedBatches = []int{1}
	spec := measurementInstanceSpec(node, profile)
	nativeClient, _ := preparationNativeHTTPClient(t, ctx, spec.Config, profile)
	manager, kube, measure := measurementOperations(t, ctx, fixture, node, class, spec, nativeClient)
	manager.store.client = beoStatusClient{Client: manager.store.client}
	prepared := measure(servingstate.Operation{ID: "session.1.prepare", Kind: servingstate.OperationPrepare, Node: spec.Instance.Node, Prepare: &spec})
	pod := &core.Pod{}
	if err := manager.store.client.Get(ctx, types.NamespacedName{Namespace: fixture.Namespace, Name: spec.Name}, pod); err != nil {
		t.Fatal(err)
	}
	assertMeasurementInstance(t, ctx, kube, pod, prepared, 1)
	id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
	root := &edge.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{
		Namespace: fixture.Namespace, Name: "session", UID: "session-uid", Generation: 1,
		Annotations: map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Now())}},
		Status: edge.BatchEnergyOptimizationStatus{Phase: string(edge.MeasurementPhaseFailed), Outcome: "FatalFailure", ObservedGeneration: 1}}
	root.Spec.State.TargetNode = node.Name
	root.Spec.Session = sessionSettingsFixture()
	if err := manager.store.client.Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	runnerService := &core.Service{ObjectMeta: meta.ObjectMeta{Namespace: fixture.Namespace, Name: "session-runner",
		Labels: map[string]string{runnerNodeLabel: string(node.UID)}}}
	if _, err := kube.CoreV1().Services(fixture.Namespace).Create(ctx, runnerService, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	operations, err := servingops.NewOperations(manager.reservations, manager.store, kube, manager.runtime, manager.observer, manager.observer.native,
		servingops.OperationConfig{Namespace: fixture.Namespace, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewSchedulerController(manager.reservations, manager.store, kube, manager.runtime, manager.observer, operations, manager.observationLog,
		SchedulerControllerConfig{
			Settings: withSchedulerEstimateFixture(serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second, MaxReplicas: 1, MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1,
				CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
				PlanSearchStrategy: "v1", PlanSearchEvaluations: 500000, PlanExactFinalists: 16, PlanPolishActive: 4,
				CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1}),
			MeasurementNamespaces: []string{fixture.Namespace},
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	recoveryBootRuntime(t, controller)
	controller.measurement.Runtime = controller.runtime
	h := &admissionHarness{t: t, controller: controller, kube: kube, node: node}
	if !controller.runtime.Recovery().Unknown || !controller.runtime.InstanceRecoveryUnknown(id) {
		t.Fatal("restart did not fence the leftover physical executor")
	}
	h.admit()
	results := controller.State().Results
	if h.holder() != string(root.UID) || len(results) != 1 || !strings.Contains(results[0].Error, "executor recovery unknown") {
		t.Fatalf("unverified executor lost its hold: holder=%q results=%+v", h.holder(), results)
	}
	if err := manager.store.client.Get(ctx, client.ObjectKeyFromObject(root), root); err != nil {
		t.Fatal(err)
	}
	if condition := apimeta.FindStatusCondition(root.Status.Conditions, "NodeReturned"); condition == nil ||
		condition.Status != meta.ConditionFalse || !strings.Contains(condition.Message, "UnconfirmedInstance: executor recovery unknown") {
		t.Fatalf("terminal session hid its held-node reason: %+v", root.Status.Conditions)
	}
	if _, err := kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{}); err != nil {
		t.Fatal("unverified executor was removed", err)
	}
	if _, err := operations.AdmittedNode(ctx, spec.Instance.Node); err != servingops.ErrNodeInMeasurement {
		t.Fatalf("serving operation admitted to measurement pool: %v", err)
	}
	if _, err := controller.measurement.AdmittedNode(ctx, spec.Instance.Node); err != nil {
		t.Fatalf("measurement operation refused its held Node: %v", err)
	}
	if err := manager.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	physical := schedulerPhysicalState(id, controller.runtime.Instances())
	if physical == nil || physical.RecoveryUnknown || !physical.NativeVerified || physical.Native == nil ||
		!physical.Native.Ready || physical.Native.Metadata.Mode != "profiling" || len(physical.Native.Requests) != 0 {
		t.Fatalf("fresh profiling executor did not release exact recovery: %+v issues=%+v", physical, manager.observer.Issues())
	}
	h.admit()
	if h.holder() != "" {
		t.Fatalf("verified failed session kept its taint: %+v", controller.State().Results)
	}
	if _, err := kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("leftover measurement Pod survived hand-back: %v", err)
	}
	for _, name := range []string{servingstate.InstanceServicePrefix + string(pod.UID), runnerService.Name} {
		if _, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("Service %s survived hand-back: %v", name, err)
		}
	}
	if physical := schedulerPhysicalState(id, controller.runtime.Instances()); physical != nil {
		t.Fatalf("removed executor retained by runtime: %+v", physical)
	}
	if err := manager.store.client.Get(ctx, client.ObjectKeyFromObject(root), root); err != nil {
		t.Fatal(err)
	}
	if root.Status.Phase != string(edge.MeasurementPhaseFailed) || root.Status.Outcome != "FatalFailure" {
		t.Fatalf("terminal session was restarted: %+v", root.Status)
	}
	if condition := apimeta.FindStatusCondition(root.Status.Conditions, "NodeReturned"); condition == nil ||
		condition.Status != meta.ConditionTrue {
		t.Fatalf("terminal session still reports its Node held: %+v", root.Status.Conditions)
	}
	completed := slices.ContainsFunc(controller.State().Results, func(result servingstate.OperationResult) bool {
		return result.Operation.Kind == handBackKind && result.Status == servingstate.NativeOperationComplete &&
			slices.Contains(result.Progress, "serving Pods removed") && slices.Contains(result.Progress, "taint removed")
	})
	if !completed {
		t.Fatalf("hand-back did not complete: %+v", controller.State().Results)
	}
}

func TestEvidenceReadersCountInvalidMeasurements(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	invalid := map[string]string{profilingrun.InvalidAnnotation: "PowerChangedDuringMeasurement power"}
	campaign := &edge.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "campaign", Generation: 1, Annotations: invalid},
		Status: edge.BatchEnergyOptimizationStatus{Phase: catalogCompletePhase, ObservedGeneration: 1}}
	transition := &edge.PowerModeTransitionCost{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "power", UID: "power-uid", Generation: 1, Annotations: invalid},
		Status: edge.PowerModeTransitionCostStatus{ObservedGeneration: 1, Phase: edge.PowerModeTransitionCostPhaseComplete,
			Result: &edge.ProfilingRunEvidenceReference{Store: "store", Key: "power/result.json", Size: 1}}}
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(campaign, transition).Build()}
	catalogObjects := &catalogTestObjects{}
	catalog, err := NewCatalog(store, catalogObjects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(context.Background(), []string{"chill-system"})
	if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || view.Insufficiencies[0].Code != measurementInvalidCode || catalogObjects.opens != 0 {
		t.Fatalf("invalid campaign used: %+v %v", view, err)
	}
	costObjects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
	evidence, err := NewCostEvidence(store, costObjects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	costs, err := evidence.Refresh(context.Background(), []string{"chill-system"}, nil)
	if err != nil || len(costs.Costs) != 0 || len(costs.Issues) != 1 || costs.Issues[0].Code != measurementInvalidCode {
		t.Fatalf("invalid transition cost used: %+v %v", costs, err)
	}
}

const (
	lowMode = "eco"
	maxMode = "max"
	// nvpModel0 is the read-back of JetsonNVPModel ID 0.
	nvpModel0 = `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":0}}`
)

// The hand-back requests the lowest mode on the Node it still holds and removes
// the taint once the agent settled it.
func TestHandBackSetsTheLowestModeBeforeRemovingTheTaint(t *testing.T) {
	// Every Failed session is terminal: ChillServer refuses an unresolved
	// session at admission instead of retrying it under a held Node.
	for _, reason := range []string{"", "InvalidRunSpec", "FatalFailure", "RetryLimitExhausted"} {
		t.Run("failure="+reason, func(t *testing.T) {
			h := newAdmissionHarness(t)
			actuator := edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 2}}
			class := &edge.DeviceClass{ObjectMeta: meta.ObjectMeta{Name: "cpu"}, Spec: edge.DeviceClassSpec{NodeSelector: map[string]string{"hardware": "cpu"},
				Architecture: "amd64", PowerModes: []edge.PowerMode{{Name: lowMode, Actuator: actuator}}}}
			h.create(class)
			h.controller.mu.Lock()
			h.controller.config.PowerPolicies = []nodeactuator.ActuationPolicySpec{{Name: lowMode, DeviceClass: "cpu", PowerMode: lowMode, ActuatorKind: string(actuator.Kind), SettlingDurationSeconds: 1, MaxActuationDurationSeconds: 60}}
			h.controller.config.Baselines = []serving.NodeBaseline{{ID: "idle", Node: serving.NodeReference{Name: "node", UID: "node-uid"}, PowerMode: lowMode, Watts: 5,
				Meter: "wall", Scope: servingstate.MeasurementWallScope, Reference: evidencestore.ObjectIdentity{Store: "evidence", Key: "idle", Size: 1}}}
			h.controller.mu.Unlock()
			holder := "deleted-uid"
			if reason != "" {
				root := &edge.BatchEnergyOptimization{
					ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "failed", UID: "held-uid", Generation: 1,
						Annotations: map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Now())}},
					Status: edge.BatchEnergyOptimizationStatus{Phase: "Failed", Outcome: reason, ObservedGeneration: 1,
						Conditions: []meta.Condition{{Type: "MeasurementComplete", Status: meta.ConditionFalse, Reason: reason, ObservedGeneration: 1}}},
				}
				root.Spec.State.TargetNode = h.node.Name
				h.create(root)
				holder = string(root.UID)
			}
			h.setNode(func(n *core.Node) {
				n.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: holder, Effect: core.TaintEffectNoSchedule}}
			})
			settle := settleAgent(class)
			holderAtRequest := ""
			h.nodes.agent = func(n *core.Node) {
				holderAtRequest, _ = profilingrun.MeasurementHolder(n)
				settle(n)
			}
			h.admit()
			node, err := h.kube.CoreV1().Nodes().Get(context.Background(), "node", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if holderAtRequest != holder || node.Labels[chillmeta.PowerMode] != lowMode || h.holder() != "" {
				t.Fatalf("hand-back requested %q under %q, taint %q: %+v", node.Labels[chillmeta.PowerMode], holderAtRequest, h.holder(), h.controller.State().Results)
			}
		})
	}
}

// cpuClass is the DeviceClass of the fixture Node: "max" declares its online
// CPUs and "low" does not.
func cpuClass() *edge.DeviceClass {
	actuator := func(id int32) edge.PowerModeActuator {
		return edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: id}}
	}
	return &edge.DeviceClass{ObjectMeta: meta.ObjectMeta{Name: "cpu"}, Spec: edge.DeviceClassSpec{NodeSelector: map[string]string{"hardware": "cpu"},
		Architecture: "amd64", PowerModes: []edge.PowerMode{{Name: "max", Actuator: actuator(0), OnlineCPUs: "0-5"}, {Name: "low", Actuator: actuator(1)}}}}
}

func TestAdmitSharedCPUWithoutOnlineInventory(t *testing.T) {
	for _, mode := range []string{"max", "low"} {
		t.Run(mode, func(t *testing.T) {
			h := newAdmissionHarness(t)
			h.create(cpuClass())
			h.setNode(func(node *core.Node) { delete(node.Annotations, chillmeta.OnlineCPUs) })
			// A Profiler-run root: ChillServer grants it and runs nothing itself.
			root := baselineRoot("baseline", time.Now())
			root.Spec.State.Candidate.PowerMode = mode
			h.create(root)
			h.admit()
			if got := h.admission(root); !strings.HasPrefix(got, "Granted") || h.holder() != string(root.UID) {
				t.Fatalf("shared CPU admission required allocation inventory: %q holder=%q", got, h.holder())
			}
		})
	}
}
