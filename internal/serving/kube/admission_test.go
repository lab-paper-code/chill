package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type admissionHarness struct {
	t          *testing.T
	controller *SchedulerController
	kube       *kubefake.Clientset
	node       *core.Node
}

func newAdmissionHarness(t *testing.T, kubeObjects ...client.Object) *admissionHarness {
	t.Helper()
	manager, _ := newManagerHarness(t)
	_, node, _, _ := observerSourceFixture()
	objects := []runtime.Object{node}
	for _, o := range kubeObjects {
		objects = append(objects, o)
	}
	kube := kubefake.NewSimpleClientset(objects...)
	manager.observer.kube = kube
	operations, err := NewOperations(manager.store, kube, manager.runtime, manager.observer, manager.observer.native,
		OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewSchedulerController(manager.store, kube, manager.runtime, manager.observer, operations, manager.observationLog,
		SchedulerControllerConfig{
			Settings: serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
				MaxObservationAge: time.Second, MaxReplicas: 1, MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1,
				CapacityPassingWindows: 1, CapacityMinimumRequests: 1, MaxCPUs: 4},
			MeasurementNamespaces: []string{"chill-system"},
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	return &admissionHarness{t: t, controller: controller, kube: kube, node: node}
}

func (h *admissionHarness) create(objects ...client.Object) {
	h.t.Helper()
	for _, o := range objects {
		if err := h.controller.store.client.Create(context.Background(), o); err != nil {
			h.t.Fatal(err)
		}
	}
}

// admit runs one cycle and waits for the runs it started.
func (h *admissionHarness) admit() {
	h.t.Helper()
	if err := h.controller.Admit(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.controller.nodeBusy(h.node) {
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

func (h *admissionHarness) annotation(object client.Object, key string) string {
	h.t.Helper()
	if err := h.controller.store.client.Get(context.Background(), client.ObjectKeyFromObject(object), object); err != nil {
		h.t.Fatal(err)
	}
	return object.GetAnnotations()[key]
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
	o.Spec.State.TargetNode = "node"
	return o
}

func servingPod(name string) *core.Pod {
	return &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: name, UID: types.UID(name + "-uid"),
		Labels: map[string]string{"app.kubernetes.io/component": servingInstanceComponent}}, Spec: core.PodSpec{NodeName: "node"}}
}

func TestAdmitGrantsEmptyNodeAndIgnoresPodsCHILLDidNotPlace(t *testing.T) {
	external := &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: "default", Name: "plan10-serving-jetsono"}, Spec: core.PodSpec{NodeName: "node"}}
	h := newAdmissionHarness(t, external)
	root := baselineRoot("idle", time.Now())
	h.create(root)
	h.admit()
	if h.holder() != string(root.UID) {
		t.Fatalf("taint holder = %q", h.holder())
	}
	if granted, _, _ := profilingrun.Admission(map[string]string{profilingrun.AdmissionAnnotation: h.annotation(root, profilingrun.AdmissionAnnotation)}); !granted {
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
	if got := h.annotation(root, profilingrun.AdmissionAnnotation); got != "Refused: NodeServing chill-system/serving-a" {
		t.Fatalf("admission = %q", got)
	}
	if h.holder() != "" {
		t.Fatal("serving Node tainted")
	}
}

func TestAdmitRefusesMissingNode(t *testing.T) {
	h := newAdmissionHarness(t)
	root := baselineRoot("idle", time.Now())
	root.Spec.State.TargetNode = "gone"
	h.create(root)
	h.admit()
	if got := h.annotation(root, profilingrun.AdmissionAnnotation); got != "Refused: NodeMissing gone" {
		t.Fatalf("admission = %q", got)
	}
}

func TestAdmitQueuesBehindTheHolderAndItsServingPods(t *testing.T) {
	h := newAdmissionHarness(t)
	first, second := baselineRoot("first", time.Now().Add(-time.Minute)), baselineRoot("second", time.Now())
	h.create(second, first)
	h.admit()
	if h.holder() != string(first.UID) || h.annotation(second, profilingrun.AdmissionAnnotation) != "" {
		t.Fatalf("holder %q, second %q", h.holder(), h.annotation(second, profilingrun.AdmissionAnnotation))
	}
	// The holder's own serving Pod does not refuse the queued measurement.
	if _, err := h.kube.CoreV1().Pods("chill-system").Create(context.Background(), servingPod("runner"), meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.admit()
	if h.annotation(second, profilingrun.AdmissionAnnotation) != "" {
		t.Fatal("queued measurement refused while the Node is held")
	}
	h.complete(first)
	h.admit() // hand-back removes the measurement's serving Pod and the taint
	if pods, _ := h.kube.CoreV1().Pods("chill-system").List(context.Background(), meta.ListOptions{}); len(pods.Items) != 0 {
		t.Fatalf("hand-back left serving Pods: %d", len(pods.Items))
	}
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
	root := baselineRoot("idle", time.Now())
	h.create(root)
	release := make(chan struct{})
	h.controller.startNodeRun(Operation{ID: "prepare", Kind: operationPrepare, Node: serving.NodeReference{Name: "node", UID: "node-uid"}}, SchedulerControllerConfig{},
		func(context.Context, *OperationResult) error { <-release; return nil })
	if err := h.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.holder() != "" {
		t.Fatal("granted a Node with a running run")
	}
	close(release)
	pending := &edge.NodeActuation{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "power"},
		Spec: edge.NodeActuationSpec{Node: edge.ProfilingRunObjectReference{Name: "node", UID: "node-uid"}, Deadline: meta.NewTime(time.Now().Add(time.Hour))}}
	h.create(pending)
	h.admit()
	if h.holder() != "" {
		t.Fatal("granted a Node with a pending power change")
	}
	pending.Status.Phase = edge.NodeActuationPhaseReady
	if err := h.controller.store.client.Update(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	h.admit()
	if h.holder() != string(root.UID) {
		t.Fatal("not granted after the power change settled")
	}
}

func TestHandBackMarksForeignPowerChangeInvalid(t *testing.T) {
	for _, owned := range []bool{false, true} {
		h := newAdmissionHarness(t)
		root := baselineRoot("idle", time.Now())
		h.create(root)
		h.admit()
		applied := meta.NewMicroTime(time.Now())
		act := &edge.NodeActuation{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "power"},
			Spec:   edge.NodeActuationSpec{Node: edge.ProfilingRunObjectReference{Name: "node", UID: "node-uid"}},
			Status: edge.NodeActuationStatus{Phase: edge.NodeActuationPhaseReady, ApplyStartedAt: &applied}}
		if owned {
			act.OwnerReferences = []meta.OwnerReference{{Kind: "ModelLoadedIdleBaseline", Name: "idle", UID: root.UID}}
		}
		h.create(act)
		h.complete(root)
		h.admit()
		invalid := h.annotation(root, profilingrun.InvalidAnnotation)
		if owned != (invalid == "") || !owned && invalid != "PowerChangedDuringMeasurement power" || h.holder() != "" {
			t.Fatalf("owned=%v invalid=%q holder=%q", owned, invalid, h.holder())
		}
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
	catalog, err := NewCatalog(store, catalogObjects, 1024, 0.95)
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
	costs, err := evidence.Refresh(context.Background(), []string{"chill-system"}, nil, nil)
	if err != nil || len(costs.Costs) != 0 || len(costs.Issues) != 1 || costs.Issues[0].Code != measurementInvalidCode {
		t.Fatalf("invalid transition cost used: %+v %v", costs, err)
	}
}

const lowMode = "eco"

func TestHandBackSetsTheLowestModeBeforeRemovingTheTaint(t *testing.T) {
	h := newAdmissionHarness(t)
	actuator := edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 2}}
	class := &edge.DeviceClass{ObjectMeta: meta.ObjectMeta{Name: "cpu"}, Spec: edge.DeviceClassSpec{NodeSelector: map[string]string{"hardware": "cpu"},
		Architecture: "amd64", PowerModes: []edge.PowerMode{{Name: lowMode, Actuator: actuator}}}}
	h.create(class)
	h.controller.mu.Lock()
	h.controller.config.PowerPolicies = []nodeactuator.ActuationPolicySpec{{Name: lowMode, DeviceClass: "cpu", PowerMode: lowMode, ActuatorKind: string(actuator.Kind), SettlingDurationSeconds: 1, MaxActuationDurationSeconds: 60}}
	h.controller.config.Baselines = []serving.NodeBaseline{{ID: "idle", Node: serving.NodeReference{Name: "node", UID: "node-uid"}, PowerMode: lowMode, Watts: 5,
		Meter: "wall", Scope: measurementWallScope, Reference: evidencestore.ObjectIdentity{Store: "evidence", Key: "idle", Size: 1}}}
	h.controller.mu.Unlock()
	node := h.node.DeepCopy()
	node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "deleted-uid", Effect: core.TaintEffectNoSchedule}}
	if _, err := h.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Play the actuator: read the requested mode back once the request exists.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var acts edge.NodeActuationList
		if err := h.controller.store.client.List(context.Background(), &acts); err != nil {
			t.Fatal(err)
		}
		if len(acts.Items) == 1 {
			if h.holder() != "deleted-uid" {
				t.Fatal("taint removed before the lowest mode was read back")
			}
			a := &acts.Items[0]
			if a.Spec.PowerMode != lowMode || a.Labels[handBackLabel] == "" {
				t.Fatalf("hand-back requested %q", a.Spec.PowerMode)
			}
			ready := meta.NewMicroTime(time.Now())
			a.Status = edge.NodeActuationStatus{Phase: edge.NodeActuationPhaseReady, ObservedGeneration: a.Generation, ReadyAt: &ready,
				TargetObservation: &edge.NodeActuationObservation{PowerMode: lowMode, Actuator: actuator}}
			if err := h.controller.store.client.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hand-back requested no power change: %+v", h.controller.State().Results)
		}
		time.Sleep(time.Millisecond)
	}
	h.admit()
	if h.holder() != "" {
		t.Fatalf("taint kept: %+v", h.controller.State().Results)
	}
}

// A retried hand-back finds the lowering of the failed attempt before it inside
// the measurement window; that hand-back power change does not invalidate it.
func TestForeignActuationsSkipHandBackLowering(t *testing.T) {
	from := time.Now()
	applied := meta.NewMicroTime(from.Add(time.Second))
	act := func(name string, labels map[string]string) edge.NodeActuation {
		return edge.NodeActuation{ObjectMeta: meta.ObjectMeta{Name: name, Labels: labels},
			Spec:   edge.NodeActuationSpec{Node: edge.ProfilingRunObjectReference{Name: "node", UID: "node-uid"}},
			Status: edge.NodeActuationStatus{ApplyStartedAt: &applied}}
	}
	acts := []edge.NodeActuation{act("lowering", map[string]string{handBackLabel: holderLabelValue}), act("operator", nil)}
	if got := foreignActuations(acts, "node-uid", "root-uid", from, from.Add(time.Minute)); len(got) != 1 || got[0] != "operator" {
		t.Fatalf("foreign = %v", got)
	}
}

// Measurements request power changes in their own namespace; admission waits
// for those too.
func TestAdmitWaitsForPendingPowerChangeOfAMeasurementNamespace(t *testing.T) {
	h := newAdmissionHarness(t)
	h.controller.mu.Lock()
	h.controller.config.MeasurementNamespaces = []string{"chill-system", "measurements"}
	h.controller.mu.Unlock()
	root := baselineRoot("idle", time.Now())
	h.create(root)
	pending := &edge.NodeActuation{ObjectMeta: meta.ObjectMeta{Namespace: "measurements", Name: "power"},
		Spec: edge.NodeActuationSpec{Node: edge.ProfilingRunObjectReference{Name: "node", UID: "node-uid"}, Deadline: meta.NewTime(time.Now().Add(time.Hour))}}
	h.create(pending)
	h.admit()
	if h.holder() != "" {
		t.Fatal("granted a Node with a pending power change in a measurement namespace")
	}
	pending.Status.Phase = edge.NodeActuationPhaseReady
	if err := h.controller.store.client.Update(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	h.admit()
	if h.holder() != string(root.UID) {
		t.Fatal("not granted after the power change settled")
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

// A measurement whose CPUs its mode cannot leave outside the serving
// reservation is refused before the Node is taken; the option it would
// measure could never be placed.
func TestAdmitRefusesCPUsTheModeCannotOffer(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     string
		affinity []int32
		readBack string
		want     string
	}{
		{name: "declared affinity fits", mode: "max", affinity: []int32{2, 3, 4, 5}, want: "Granted"},
		{name: "declared affinity offline", mode: "max", affinity: []int32{4, 5, 6}, want: "Refused: CPUsUnavailable 4-6 max available 2-5"},
		{name: "affinity on a reserved CPU", mode: "max", affinity: []int32{1, 2}, want: "Refused: CPUsUnavailable"},
		{name: "undeclared mode not read back", mode: "low", affinity: []int32{2}, want: "Refused: OnlineCPUsUnknown low"},
		{name: "undeclared mode read back uses the Node", mode: "low", affinity: []int32{2, 3}, readBack: "low", want: "Granted"},
		{name: "undeclared read-back mode too small", mode: "low", affinity: []int32{2, 3, 4}, readBack: "low", want: "Refused: CPUsUnavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAdmissionHarness(t)
			h.create(cpuClass())
			h.controller.mu.Lock()
			h.controller.config.Reserved = map[string]serving.SchedulerAllocation{"node": {CPUs: []int{0, 1}}}
			h.controller.mu.Unlock()
			if test.readBack != "" {
				ready := &edge.NodeActuation{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "read-back"},
					Spec:   edge.NodeActuationSpec{Node: edge.ProfilingRunObjectReference{Name: "node", UID: "node-uid"}, PowerMode: test.readBack},
					Status: edge.NodeActuationStatus{Phase: edge.NodeActuationPhaseReady, TargetObservation: &edge.NodeActuationObservation{PowerMode: test.readBack}}}
				h.create(ready)
			}
			root := beoRoot("beo", "node", "")
			root.Spec.State.Candidate.PowerMode, root.Spec.CPUAffinity = test.mode, test.affinity
			h.create(root)
			h.admit()
			if got := h.annotation(root, profilingrun.AdmissionAnnotation); !strings.HasPrefix(got, test.want) {
				t.Fatalf("admission = %q, want %q", got, test.want)
			}
			if granted := strings.HasPrefix(test.want, "Granted"); granted != (h.holder() == string(root.UID)) {
				t.Fatalf("taint holder %q", h.holder())
			}
		})
	}
}

// A refusal is terminal and does not hold the Node's queue: the next root on
// the Node is granted in the same cycle.
func TestAdmitRefusalLetsTheNextRootGo(t *testing.T) {
	h := newAdmissionHarness(t)
	h.create(cpuClass())
	first, second := beoRoot("first", "node", ""), baselineRoot("second", time.Now())
	first.Spec.State.Candidate.PowerMode, first.Spec.CPUAffinity = "max", []int32{7}
	h.create(first, second)
	h.admit()
	if got := h.annotation(first, profilingrun.AdmissionAnnotation); !strings.HasPrefix(got, "Refused: CPUsUnavailable") {
		t.Fatalf("first = %q", got)
	}
	if h.holder() != string(second.UID) {
		t.Fatalf("holder %q", h.holder())
	}
}
