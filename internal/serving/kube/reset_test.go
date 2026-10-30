package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestDecodeManualOperationStrictKindFields(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"prepare","workloadUID":"w","profileID":"p","node":{"name":"n","uid":"u"},"reason":"prepare"}`,
		`{"kind":"prepare","workloadUID":"w","profileID":"p","node":{"name":"n","uid":"u"},"maxBatch":1,"reason":"restore comparison state"}`,
		`{"kind":"remove","workloadUID":"w","node":{"name":"n","uid":"u"},"instance":"p/c","reason":"remove"}`,
		`{"kind":"route","workloadUID":"w","weights":{"p/c":1},"reason":"route"}`,
		`{"kind":"unroute","workloadUID":"w","reason":"unroute"}`,
		`{"kind":"release","workloadUID":"w","reason":"release"}`,
		`{"kind":"reset","workloadUIDs":["w"],"reason":"reset"}`,
		`{"kind":"reset","scope":"all-non-autonomous","reason":"reset"}`,
	} {
		if _, err := DecodeManualOperation(json.RawMessage(raw)); err != nil {
			t.Errorf("valid request refused: %s: %v", raw, err)
		}
	}
	for _, tc := range []struct{ name, raw, field string }{
		{"prepare weights null", `{"kind":"prepare","reason":"test","weights":null}`, "weights"},
		{"prepare scope empty", `{"kind":"prepare","reason":"test","scope":""}`, "scope"},
		{"remove profile empty", `{"kind":"remove","reason":"test","profileID":""}`, "profileID"},
		{"remove batch zero", `{"kind":"remove","reason":"test","fixedBatchSize":0}`, "fixedBatchSize"},
		{"remove max batch zero", `{"kind":"remove","reason":"test","maxBatch":0}`, "maxBatch"},
		{"route max batch null", `{"kind":"route","reason":"test","maxBatch":null}`, "maxBatch"},
		{"route node empty", `{"kind":"route","reason":"test","node":{}}`, "node"},
		{"route node null", `{"kind":"route","reason":"test","node":null}`, "node"},
		{"unroute weights empty", `{"kind":"unroute","reason":"test","weights":{}}`, "weights"},
		{"release instance empty", `{"kind":"release","reason":"test","instance":""}`, "instance"},
		{"reset UID empty", `{"kind":"reset","reason":"test","workloadUID":""}`, "workloadUID"},
		{"reset node null", `{"kind":"reset","reason":"test","node":null}`, "node"},
		{"caller actor", `{"kind":"reset","reason":"test","actorUsername":"operator"}`, "actorUsername"},
		{"unknown field", `{"kind":"reset","reason":"test","unexpected":null}`, "unexpected"},
		{"missing kind", `{"reason":"test"}`, "kind"},
		{"null kind", `{"kind":null,"reason":"test"}`, "kind"},
		{"unknown kind", `{"kind":"power","reason":"test"}`, "kind"},
		{"numeric kind", `{"kind":1,"reason":"test"}`, ""},
		{"missing reason", `{"kind":"reset"}`, "reason"},
		{"null reason", `{"kind":"reset","reason":null}`, "reason"},
		{"blank reason", `{"kind":"reset","reason":" \t\n"}`, "reason"},
		{"numeric reason", `{"kind":"reset","reason":1}`, ""},
		{"array", `[]`, ""},
		{"null", `null`, "kind"},
		{"trailing JSON", `{"kind":"reset","reason":"test"} {}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeManualOperation(json.RawMessage(tc.raw))
			var domain *serving.Error
			if !errors.As(err, &domain) || domain.Code != "InvalidBody" || tc.field != "" && domain.Field != tc.field {
				t.Fatalf("request did not fail structurally: %s: %v", tc.raw, err)
			}
		})
	}
}

// resetAPI supplies API-server visibility between the native-object fake and
// clientset, including disappearance after the real source finalizer is removed.
// It does not manufacture drain or executor termination evidence.
type resetAPI struct {
	client.Client
	kube              *kubefake.Clientset
	podWrites         int
	deleteCalls       int
	refuseDelete      bool
	terminateOnDelete bool
}

func (c *resetAPI) syncPod(ctx context.Context, pod *core.Pod) error {
	current := &core.Pod{}
	err := c.Client.Get(ctx, client.ObjectKeyFromObject(pod), current)
	resource := core.SchemeGroupVersion.WithResource("pods")
	if apierrors.IsNotFound(err) {
		return c.kube.Tracker().Delete(resource, pod.Namespace, pod.Name)
	}
	if err != nil {
		return err
	}
	return c.kube.Tracker().Update(resource, current, current.Namespace)
}

func (c *resetAPI) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*core.Pod); ok {
		c.podWrites++
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *resetAPI) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	if pod, ok := obj.(*core.Pod); ok {
		c.podWrites++
		return c.syncPod(ctx, pod)
	}
	return nil
}

func (c *resetAPI) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	pod, isPod := obj.(*core.Pod)
	if isPod {
		c.deleteCalls++
		if c.refuseDelete {
			return errors.New("owned Pod deletion refused")
		}
	}
	if err := c.Client.Delete(ctx, obj, opts...); err != nil {
		return err
	}
	if isPod && c.terminateOnDelete {
		current := &core.Pod{}
		if err := c.Client.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
			return err
		}
		status := firstfloorStatus(current)
		status.Ready = false
		status.State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ContainerID: status.ContainerID, ExitCode: 0}}
		current.Status.Phase = core.PodSucceeded
		if err := c.Client.Status().Update(ctx, current); err != nil {
			return err
		}
	}
	if isPod {
		c.podWrites++
		return c.syncPod(ctx, pod)
	}
	return nil
}

type resetHarness struct {
	controller              *SchedulerController
	api                     *resetAPI
	kube                    *kubefake.Clientset
	owned, foreign          *core.Pod
	selectedUID, foreignUID string
	updates                 int
	failUpdate              int
}

func newResetHarness(t *testing.T) *resetHarness {
	t.Helper()
	ctx := context.Background()
	manager, _ := newManagerHarness(t)
	createManagerCatalog(t, manager)
	selected, err := manager.CreateWorkload(ctx, "selected", managerWorkloadSpec())
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := manager.CreateWorkload(ctx, "foreign", managerWorkloadSpec())
	if err != nil {
		t.Fatal(err)
	}
	owned, node, _, class := observerSourceFixture()
	owned.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", Name: "selected", UID: types.UID(selected.Applied.UID)}}
	owned.Status.Phase = core.PodSucceeded
	status := firstfloorStatus(owned)
	status.Ready = false
	status.State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ContainerID: status.ContainerID, ExitCode: 0}}
	other := owned.DeepCopy()
	other.Name, other.UID = "foreign-instance", "foreign-pod-uid"
	other.Labels[instancePodUIDLabel] = string(other.UID)
	other.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", Name: "foreign", UID: types.UID(foreign.Applied.UID)}}
	other.Spec.NodeName = "foreign-node"
	otherNode := node.DeepCopy()
	otherNode.Name, otherNode.UID = other.Spec.NodeName, "foreign-node-uid"
	for _, obj := range []client.Object{owned, other, &class, &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Name: owned.Name, Namespace: owned.Namespace}, Data: map[string]string{"config.json": "{}"}}} {
		// Keep the fixture's physical UID, rather than managerAPI's registration UID.
		if err := manager.store.client.(*managerAPI).Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	kube := kubefake.NewSimpleClientset(node, otherNode, owned.DeepCopy(), other.DeepCopy(), observerServiceFixture(owned), observerServiceFixture(other))
	api := &resetAPI{Client: &nodeAPI{Client: manager.store.client, kube: kube}, kube: kube}
	manager.store.client, manager.observer.kube = api, kube
	operations, err := NewOperations(manager.store, kube, manager.runtime, manager.observer, manager.observer.native,
		OperationConfig{Namespace: owned.Namespace, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	settings := serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
		MaxObservationAge: time.Second, MaxReplicas: 1, MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1,
		CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
		CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1, MaxCPUs: 4,
		AutonomousWorkloads: []string{selected.Applied.UID, foreign.Applied.UID}}
	document, err := json.Marshal(map[string]any{"settings": settings, "staticRouting": map[string]map[string]int{foreign.Applied.UID: {}}, "reserved": map[string]any{"node": map[string]any{"cpus": []int{3}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoreV1().ConfigMaps(owned.Namespace).Create(ctx, &core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Name: "scheduler", Namespace: owned.Namespace, ResourceVersion: "1"},
		Data:       map[string]string{"scheduler.json": string(document), "other": "preserved"},
	}, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	controller, err := NewSchedulerController(manager.store, kube, manager.runtime, manager.observer, operations, manager.observationLog,
		SchedulerControllerConfig{Settings: settings, ConfigurationName: "scheduler", ConfigurationKey: "scheduler.json", StaticRouting: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	h := &resetHarness{controller: controller, api: api, kube: kube, owned: owned, foreign: other,
		selectedUID: selected.Applied.UID, foreignUID: foreign.Applied.UID}
	kube.PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		h.updates++
		if h.failUpdate > 0 && h.updates == h.failUpdate {
			return true, nil, apierrors.NewConflict(core.Resource("configmaps"), "scheduler", errors.New("scheduler configuration write refused"))
		}
		return false, nil, nil
	})
	return h
}

func (h *resetHarness) request() ManualOperation {
	return ManualOperation{Kind: operationReset, WorkloadUIDs: []string{h.selectedUID}, Reason: "finish measured trial", ActorUsername: "operator", ActorUID: "operator-uid"}
}

func (h *resetHarness) configuration(t *testing.T) (*core.ConfigMap, map[string]json.RawMessage) {
	t.Helper()
	cm, err := h.kube.CoreV1().ConfigMaps(h.owned.Namespace).Get(context.Background(), "scheduler", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cm.Data["scheduler.json"]), &document); err != nil {
		t.Fatal(err)
	}
	return cm, document
}

func assertResetSteps(t *testing.T, result OperationResult, names ...string) {
	t.Helper()
	got := make([]string, len(result.Steps))
	for i, step := range result.Steps {
		got[i] = step.Name
		if step.StartedAt.IsZero() || step.EndedAt.Before(step.StartedAt) || step.Before.At.IsZero() || step.Actual.At.IsZero() {
			t.Errorf("step lacks timing or partial readback evidence: %+v", step)
		}
	}
	if !slices.Equal(got, names) {
		t.Fatalf("reset phases = %v, want %v", got, names)
	}
	if result.Operation.ID == "" || result.Operation.Kind != operationReset || result.StartedAt.IsZero() ||
		result.EndedAt.Before(result.StartedAt) || result.Before.At.IsZero() || result.Actual.At.IsZero() {
		t.Errorf("reset lost root operation envelope: %+v", result)
	}
}

func TestResetTwiceCleansOnlyOwnedPhysicalInstancesAC33(t *testing.T) {
	h := newResetHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, beforeDocument := h.configuration(t)
	first, err := h.controller.ExecuteManual(ctx, h.request())
	if err != nil || first.Status != nativeOperationComplete || first.Error != "" {
		t.Fatalf("first reset failed: %+v err=%v", first, err)
	}
	assertResetSteps(t, first, "disable-autonomy", "unroute", "drain", "remove", "release", "lower-power")
	for _, step := range first.Steps {
		if step.Status != nativeOperationComplete || step.Error != "" {
			t.Errorf("successful reset has unsuccessful phase: %+v", step)
		}
	}
	if !slices.Equal(first.Operation.WorkloadUIDs, []string{h.selectedUID}) || first.Operation.Reason != h.request().Reason ||
		first.Operation.ActorUsername != "operator" || first.Operation.ActorUID != "operator-uid" {
		t.Errorf("reset lost accepted scope or caller: %+v", first.Operation)
	}
	for _, get := range []func() error{
		func() error {
			_, err := h.kube.CoreV1().Pods(h.owned.Namespace).Get(ctx, h.owned.Name, meta.GetOptions{})
			return err
		},
		func() error { return h.api.Get(ctx, client.ObjectKeyFromObject(h.owned), &core.Pod{}) },
		func() error {
			_, err := h.kube.CoreV1().Services(h.owned.Namespace).Get(ctx, instanceService(h.owned).Name, meta.GetOptions{})
			return err
		},
		func() error { return h.api.Get(ctx, client.ObjectKeyFromObject(h.owned), &core.ConfigMap{}) },
	} {
		if err := get(); !apierrors.IsNotFound(err) {
			t.Errorf("owned executor resource survives reset: %v", err)
		}
	}
	foreign, err := h.kube.CoreV1().Pods(h.foreign.Namespace).Get(ctx, h.foreign.Name, meta.GetOptions{})
	if err != nil || !reflect.DeepEqual(foreign.OwnerReferences, h.foreign.OwnerReferences) {
		t.Fatalf("foreign ownership changed: %+v %v", foreign, err)
	}
	if _, err := h.kube.CoreV1().Services(h.foreign.Namespace).Get(ctx, instanceService(h.foreign).Name, meta.GetOptions{}); err != nil {
		t.Fatal("reset removed foreign executor Service", err)
	}
	cm, document := h.configuration(t)
	var settings serving.SchedulerSettings
	var routing map[string]map[string]int
	if err := json.Unmarshal(document["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(document["staticRouting"], &routing); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(settings.AutonomousWorkloads, []string{h.foreignUID}) || !slices.Equal(h.controller.Settings().AutonomousWorkloads, []string{h.foreignUID}) {
		t.Fatalf("reset disabled another Workload or did not apply settings: %+v", settings)
	}
	if _, ok := routing[h.selectedUID]; ok {
		t.Fatal("selected declaration remains after release")
	}
	if _, ok := routing[h.foreignUID]; !ok || cm.Data["other"] != "preserved" || !reflect.DeepEqual(document["reserved"], beforeDocument["reserved"]) {
		t.Fatalf("reset changed foreign declaration or unrelated configuration: %+v", cm.Data)
	}
	effective, _ := h.controller.runtime.SchedulerRouting()
	if len(effective[h.selectedUID]) != 0 || len(first.Actual.Routing[h.selectedUID]) != 0 ||
		len(first.Steps[1].Actual.Routing[h.selectedUID]) != 0 {
		t.Errorf("reset ACK still routes new requests: runtime=%v actual=%v unroute=%v", effective, first.Actual.Routing, first.Steps[1].Actual.Routing)
	}
	h.assertAlreadyClean(t, ctx, cm)
}

//nolint:gocyclo // One real reset fixture covers atomic inheritance, all refusal boundaries and pre-effect responsibility.
func TestResetInheritsOnlyTerminalContainedReservationsAC38(t *testing.T) {
	const preEffectRefusal = "pre-effect-refusal"
	const transitionDirty = "transition-dirty"
	for _, owner := range []string{nativeOperationFailed, nativeOperationPartial, nativeOperationRunning, nativeOperationCancel, "measurement", "outside-workload", "outside-node", "active-sibling", preEffectRefusal, transitionDirty} {
		t.Run(owner, func(t *testing.T) {
			h := newResetHarness(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Keep the Pod visible to capture until reset actually removes it;
			// its terminated container is not a Ready/policy confirmation.
			h.owned.Status.Phase = core.PodRunning
			if _, err := h.kube.CoreV1().Pods(h.owned.Namespace).UpdateStatus(ctx, h.owned, meta.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := h.api.Status().Update(ctx, h.owned); err != nil {
				t.Fatal(err)
			}
			node, err := h.kube.CoreV1().Nodes().Get(ctx, h.owned.Spec.NodeName, meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			oldID := "manual-route:retained-reset"
			old := &actionRun{owner: oldID, reserved: true,
				action: serving.Action{Kind: operationRoute, Status: nativeOperationFailed},
				scope: resourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{h.selectedUID}, Pods: []string{string(h.owned.UID)},
					Instances: []string{physicalID(h.owned.UID, firstfloorStatus(h.owned).ContainerID)}},
				result: &OperationResult{Operation: Operation{Kind: operationRoute, WorkloadUID: h.selectedUID}, EndedAt: time.Now().Add(-time.Second), Status: nativeOperationFailed}}
			switch owner {
			case nativeOperationPartial:
				old.action.Status, old.result.Status = nativeOperationPartial, nativeOperationPartial
			case nativeOperationRunning, nativeOperationCancel:
				old.action.Status = owner
			case "measurement":
				old.action.Kind = measurementLeaseKind
			case "outside-workload":
				old.scope.Workloads = append(old.scope.Workloads, h.foreignUID)
			case "outside-node":
				old.scope.Nodes = append(old.scope.Nodes, serving.NodeReference{Name: "outside-reset", UID: "outside-reset-uid"})
			case "active-sibling":
				h.controller.runs["active-reset-sibling"] = &actionRun{owner: oldID, action: serving.Action{Status: nativeOperationRunning}}
			case transitionDirty:
				old.action.Kind = transitionRunKind
				id := old.scope.Instances[0]
				old.result.Operation.Target = &serving.SchedulerCandidate{ID: oldID}
				old.result.Before.Instances = []serving.InstanceState{{ID: id, PodUID: string(h.owned.UID), Node: ref}}
				old.mayTouch = map[string]bool{id: true}
			}
			h.controller.runs[oldID] = old
			admitted := owner == nativeOperationFailed || owner == nativeOperationPartial || owner == preEffectRefusal || owner == transitionDirty
			seen := false
			h.controller.config.OnAction = func(result OperationResult) error {
				if result.Status != nativeOperationRunning || seen {
					return nil
				}
				seen = true
				h.controller.mu.Lock()
				current := h.controller.runs[result.Operation.ID]
				transferred := !old.reserved && current != nil && current.reserved && current.inheritedReservation &&
					slices.Contains(current.scope.Pods, string(h.owned.UID))
				if owner == transitionDirty && (current == nil || current.proofParent != nil || !old.mayTouch[old.scope.Instances[0]]) {
					t.Error("reset laundered inherited dirty responsibility")
				}
				h.controller.mu.Unlock()
				if !transferred || h.controller.authorize(Operation{WorkloadUID: h.selectedUID}) == nil {
					t.Error("reset did not atomically inherit and retain the complete reservation")
				}
				if owner == preEffectRefusal {
					return errors.New("reset audit record refused")
				}
				// Kubelet publishes the already-terminated container's final Pod
				// phase after admission; reset must still delete the actual Pod.
				h.owned.Status.Phase = core.PodSucceeded
				if _, err := h.kube.CoreV1().Pods(h.owned.Namespace).UpdateStatus(ctx, h.owned, meta.UpdateOptions{}); err != nil {
					return err
				}
				if err := h.api.Status().Update(ctx, h.owned); err != nil {
					return err
				}
				return nil
			}
			result, err := h.controller.ExecuteManual(ctx, h.request())
			if !admitted {
				var conflict *serving.Error
				if !errors.As(err, &conflict) || conflict.Code != "OperationConflict" || !old.reserved || seen || h.updates != 0 {
					t.Fatalf("ineligible owner changed during reset: result=%+v err=%v held=%t", result, err, old.reserved)
				}
				return
			}
			if err != nil || !seen {
				t.Fatalf("terminal reservation blocked reset: %+v err=%v", result, err)
			}
			if owner == preEffectRefusal {
				if result.Status != nativeOperationFailed || h.controller.authorize(Operation{WorkloadUID: h.selectedUID}) == nil || h.updates != 0 {
					t.Fatalf("pre-effect reset refusal lost inherited responsibility: %+v", result)
				}
				return
			}
			if result.Status != nativeOperationComplete || h.controller.authorize(Operation{WorkloadUID: h.selectedUID}) != nil {
				t.Fatalf("completed reset did not return inherited reservation: %+v", result)
			}
			if _, err := h.kube.CoreV1().Pods(h.owned.Namespace).Get(ctx, h.owned.Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("reservation released without actual Pod cleanup: %v", err)
			}
		})
	}
}

func (h *resetHarness) assertAlreadyClean(t *testing.T, ctx context.Context, cm *core.ConfigMap) {
	t.Helper()
	updates, podWrites := h.updates, h.api.podWrites
	actionStart := len(h.kube.Actions())
	second, err := h.controller.ExecuteManual(ctx, h.request())
	if err != nil || second.Status != nativeOperationComplete || second.Error != "" {
		t.Fatalf("already-clean reset failed: %+v err=%v", second, err)
	}
	assertResetSteps(t, second, "disable-autonomy", "unroute", "drain", "remove", "release", "lower-power")
	after, _ := h.configuration(t)
	if h.updates != updates || h.api.podWrites != podWrites || !reflect.DeepEqual(after.Data, cm.Data) {
		t.Errorf("second reset mutated declaration or Pod: config writes %d->%d Pod writes %d->%d", updates, h.updates, podWrites, h.api.podWrites)
	}
	for _, action := range h.kube.Actions()[actionStart:] {
		if action.GetVerb() == getVerb || action.GetVerb() == "list" {
			continue
		}
		t.Errorf("second reset issued a new physical/configuration request: %s %s", action.GetVerb(), action.GetResource().Resource)
	}
}

func TestResetExecutionFailureStopsWithoutRetryOrRollback(t *testing.T) {
	phases := []string{"disable-autonomy", "unroute", "drain", "remove", "release", "lower-power"}
	for _, tc := range []struct {
		phase        string
		failUpdate   int
		refuseDelete bool
	}{
		{phase: "disable-autonomy", failUpdate: 1},
		{phase: "unroute", failUpdate: 2},
		{phase: "remove", refuseDelete: true},
		{phase: "release", failUpdate: 3},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			h := newResetHarness(t)
			h.failUpdate, h.api.refuseDelete = tc.failUpdate, tc.refuseDelete
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := h.controller.ExecuteManual(ctx, h.request())
			if err != nil || result.Error == "" || result.Status != nativeOperationFailed && result.Status != nativeOperationPartial {
				t.Fatalf("physical failure did not return failed root result: %+v err=%v", result, err)
			}
			index := slices.Index(phases, tc.phase)
			assertResetSteps(t, result, phases[:index+1]...)
			for i, step := range result.Steps {
				if i < index && (step.Status != nativeOperationComplete || step.Error != "") {
					t.Errorf("earlier successful phase was changed: %+v", step)
				}
				if i == index && (step.Status == nativeOperationComplete || step.Error == "") {
					t.Errorf("failure phase lacks failure evidence: %+v", step)
				}
			}
			if tc.failUpdate > 0 && h.updates != tc.failUpdate || tc.refuseDelete && h.api.deleteCalls != 1 {
				t.Errorf("failed phase retried or later declaration ran: updates=%d deletes=%d", h.updates, h.api.deleteCalls)
			}
			if tc.phase != "release" {
				if _, err := h.kube.CoreV1().Pods(h.owned.Namespace).Get(ctx, h.owned.Name, meta.GetOptions{}); err != nil {
					t.Errorf("owned Pod disappeared after an earlier phase failed: %v", err)
				}
			}
			if _, err := h.kube.CoreV1().Pods(h.foreign.Namespace).Get(ctx, h.foreign.Name, meta.GetOptions{}); err != nil {
				t.Errorf("foreign Pod was changed by failed reset: %v", err)
			}
			if tc.phase == "remove" {
				_, document := h.configuration(t)
				var routing map[string]map[string]int
				if err := json.Unmarshal(document["staticRouting"], &routing); err != nil {
					t.Fatal(err)
				}
				if weights, ok := routing[h.selectedUID]; !ok || len(weights) != 0 {
					t.Errorf("failure rolled back unroute or executed release: %v", routing)
				}
			}
		})
	}
}

func TestResetPreEffectRejectionReturnsOrRetainsReservationAC38(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown=%v", uncertain), func(t *testing.T) {
			h := newResetHarness(t)
			firstUpdate := true
			h.kube.PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
				if !firstUpdate {
					return false, nil, nil
				}
				firstUpdate = false
				if uncertain {
					return true, nil, context.DeadlineExceeded
				}
				return true, nil, apierrors.NewConflict(core.Resource("configmaps"), "scheduler", errors.New("rejected"))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			first, err := h.controller.ExecuteManual(ctx, h.request())
			if err != nil || first.Status != nativeOperationFailed {
				t.Fatalf("first reset: %+v %v", first, err)
			}
			assertResetSteps(t, first, "disable-autonomy")
			held := h.controller.authorize(Operation{WorkloadUID: h.selectedUID}) != nil
			if held != uncertain {
				t.Fatalf("pre-effect certainty=%t: reservation held=%t", !uncertain, held)
			}
			// An unknown first write still owns its scope, but a new reset may
			// atomically inherit it and prove actual cleanup rather than deadlock.
			retry, err := h.controller.ExecuteManual(ctx, h.request())
			if err != nil || retry.Status != nativeOperationComplete {
				t.Fatalf("reset could not finish retained cleanup: %+v %v", retry, err)
			}
		})
	}
}

func (h *resetHarness) powerReturnCandidate(t *testing.T) *edge.DeviceClass {
	t.Helper()
	ctx := context.Background()
	class := &edge.DeviceClass{}
	if err := h.api.Get(ctx, client.ObjectKey{Name: "cpu"}, class); err != nil {
		t.Fatal(err)
	}
	class.Spec.PowerModes = append(class.Spec.PowerModes, edge.PowerMode{Name: lowMode,
		Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 1}}})
	if err := h.api.Update(ctx, class); err != nil {
		t.Fatal(err)
	}
	workloads := h.controller.runtime.SchedulerInputs(time.Now).Workloads
	index := slices.IndexFunc(workloads, func(d *serving.Definition) bool { return d.UID == h.selectedUID })
	profile := managerProfile(workloads[index])
	profile.NodeName, profile.Condition.NodeUID, profile.Condition.PowerMode = testNode.Name, testNode.UID, class.Spec.PowerModes[0].Name
	h.controller.observer.SetProfiles([]serving.Profile{profile})
	h.controller.mu.Lock()
	h.controller.config.Preparations = map[string]PrepareSpec{profile.ID: {Instance: serving.SchedulerInstance{Node: testNode, Profile: &profile}}}
	h.controller.config.PowerPolicies = []nodeactuator.ActuationPolicySpec{{Name: lowMode, DeviceClass: class.Name, PowerMode: lowMode,
		ActuatorKind: string(edge.PowerModeActuatorJetsonNVPModel), MaxActuationDurationSeconds: 1}}
	h.controller.config.Baselines = []serving.NodeBaseline{{ID: "idle", Node: testNode, PowerMode: lowMode, Watts: 5,
		Meter: "wall", Scope: measurementWallScope, Reference: evidencestore.ObjectIdentity{Store: "evidence", Key: "idle", Size: 1}}}
	h.controller.mu.Unlock()
	return class
}

func TestResetRetriesPowerReturnAfterPodsDisappearAC33(t *testing.T) {
	for _, phase := range []string{operationRelease, "lower-power"} {
		t.Run(phase, func(t *testing.T) {
			h := newResetHarness(t)
			class := h.powerReturnCandidate(t)
			h.api.Client.(*nodeAPI).agent = settleAgent(class)
			if phase == operationRelease {
				h.failUpdate = 3
			}
			refusePower, powerRequests := true, 0
			h.kube.PrependReactor("patch", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
				powerRequests++
				if refusePower {
					return true, nil, errors.New("power request refused")
				}
				return false, nil, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			first, err := h.controller.ExecuteManual(ctx, h.request())
			if err != nil || first.Status != nativeOperationPartial || first.Error == "" ||
				first.Steps[len(first.Steps)-1].Name != phase {
				t.Fatalf("initial %s failure lost partial evidence: %+v err=%v", phase, first, err)
			}
			if _, err := h.kube.CoreV1().Pods(h.owned.Namespace).Get(ctx, h.owned.Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("remove did not finish before %s failure: %v", phase, err)
			}
			beforeRetry := powerRequests
			retry, err := h.controller.ExecuteManual(ctx, h.request())
			if err != nil || retry.Status != nativeOperationPartial || retry.Error == "" || powerRequests != beforeRetry+1 ||
				retry.Steps[len(retry.Steps)-1].Name != "lower-power" || retry.Before.Node != testNode {
				t.Fatalf("retry forgot empty Node or falsely completed: %+v requests=%d err=%v", retry, powerRequests, err)
			}
			refusePower = false
			completed, err := h.controller.ExecuteManual(ctx, h.request())
			if err != nil || completed.Status != nativeOperationComplete || completed.Error != "" || completed.Actual.PowerMode != lowMode {
				t.Fatalf("retry did not verify actual lowest mode: %+v err=%v", completed, err)
			}
			node, err := h.kube.CoreV1().Nodes().Get(ctx, testNode.Name, meta.GetOptions{})
			if err != nil || readBackMode(node, class) != lowMode || !slices.ContainsFunc(node.Status.Conditions, func(condition core.NodeCondition) bool {
				return condition.Type == core.NodeReady && condition.Status == core.ConditionTrue
			}) {
				t.Fatalf("completed reset lacks lowest-mode Ready readback: %+v err=%v", node, err)
			}
			readyIndex := slices.IndexFunc(node.Status.Conditions, func(condition core.NodeCondition) bool { return condition.Type == core.NodeReady })
			node.Status.Conditions[readyIndex].Status = core.ConditionFalse
			if _, err := h.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			notReady, err := h.controller.ExecuteManual(ctx, h.request())
			if err != nil || notReady.Status != nativeOperationPartial || notReady.Error == "" ||
				notReady.Steps[len(notReady.Steps)-1].Name != "lower-power" {
				t.Fatalf("lowest mode without Ready falsely completed: %+v err=%v", notReady, err)
			}
			node.Status.Conditions[readyIndex].Status = core.ConditionTrue
			if _, err := h.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			cm, _ := h.configuration(t)
			h.assertAlreadyClean(t, ctx, cm)
		})
	}
}

func TestResetPendingPowerTargetsRespectCurrentScopeAndOwnership(t *testing.T) {
	for _, exclude := range []string{"workload", "node UID", "excluded", "measurement", "foreign Pod"} {
		t.Run(exclude, func(t *testing.T) {
			h := newResetHarness(t)
			h.powerReturnCandidate(t)
			h.failUpdate = 3
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := h.controller.ExecuteManual(ctx, h.request())
			if err != nil || result.Status != nativeOperationPartial || result.Steps[len(result.Steps)-1].Name != operationRelease {
				t.Fatalf("could not leave pending power return: %+v err=%v", result, err)
			}
			node, err := h.kube.CoreV1().Nodes().Get(ctx, testNode.Name, meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch exclude {
			case "workload":
				profiles := h.controller.observer.Profiles()
				profiles[0].Condition.LogicalModel = "outside-scope"
				h.controller.observer.SetProfiles(profiles)
			case "node UID":
				node.UID = "reset-replacement"
			case "excluded":
				h.controller.mu.Lock()
				h.controller.config.Settings.ExcludedNodes = []string{testNode.Name}
				h.controller.mu.Unlock()
			case "measurement":
				node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "measurement", Effect: core.TaintEffectNoSchedule}}
			case "foreign Pod":
				pod := h.foreign.DeepCopy()
				pod.Spec.NodeName = testNode.Name
				if _, err := h.kube.CoreV1().Pods(pod.Namespace).Update(ctx, pod, meta.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			plan, err := h.controller.planManualResetLocked(ctx, Operation{WorkloadUIDs: []string{h.selectedUID}})
			if err != nil || len(plan.nodes) != 0 {
				t.Fatalf("pending power return crossed %s boundary: %+v err=%v", exclude, plan, err)
			}
		})
	}
}

func TestResetRejectsSnapshotOnlyPendingReservationNodeAC38(t *testing.T) {
	h := newResetHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cleaned, err := h.controller.ExecuteManual(ctx, h.request())
	if err != nil || cleaned.Status != nativeOperationComplete {
		t.Fatalf("initial cleanup failed: %+v err=%v", cleaned, err)
	}
	node, err := h.kube.CoreV1().Nodes().Get(ctx, h.owned.Spec.NodeName, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Labels[chillmeta.PowerModeState] = chillmeta.PowerModePending
	if _, err := h.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	oldID := "manual-route:pending-outside-reset"
	old := &actionRun{owner: oldID, reserved: true,
		action: serving.Action{Kind: operationRoute, Status: nativeOperationFailed},
		scope:  resourceScope{Nodes: []serving.NodeReference{ref}, Workloads: []string{h.selectedUID}},
		result: &OperationResult{Operation: Operation{Kind: operationRoute, WorkloadUID: h.selectedUID}, EndedAt: time.Now().Add(-time.Second)}}
	operation := Operation{ID: "manual-reset:pending-outside-plan", Kind: operationReset, WorkloadUIDs: []string{h.selectedUID}}
	h.controller.mu.Lock()
	h.controller.runs[oldID] = old
	h.controller.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: "stale-pending-instance", Node: ref, WorkloadUID: h.selectedUID}}
	expanded := h.controller.operationScope(operation)
	h.controller.mu.Unlock()
	if !slices.Contains(expanded.Nodes, ref) {
		t.Fatal("fixture did not expand conflict scope from the previous snapshot")
	}
	plan, err := h.controller.planManualResetLocked(ctx, operation)
	var conflict *serving.Error
	if len(plan.nodes) != 0 || !errors.As(err, &conflict) || conflict.Code != "OperationConflict" || !old.reserved {
		t.Fatalf("planning inherited a Node outside admission: plan=%+v err=%v held=%t", plan, err, old.reserved)
	}
	h.controller.mu.Lock()
	acquired := h.controller.reserveRun(operation.ID, &actionRun{action: serving.Action{Status: nativeOperationRunning}}, operation)
	held := old.reserved
	h.controller.mu.Unlock()
	if acquired || !held {
		t.Fatalf("acquisition inherited a snapshot-only Node: acquired=%t held=%t", acquired, held)
	}
}

func TestManualRoutingBarrierIsNotAnAutonomousGoalEffect(t *testing.T) {
	for _, kind := range []string{operationRoute, operationUnroute, operationRelease, operationReset} {
		t.Run(kind, func(t *testing.T) {
			controller := &SchedulerController{ctx: context.Background(), runs: map[string]*actionRun{}}
			release := make(chan struct{})
			operation := Operation{ID: "manual-" + kind, Kind: kind}
			done, result := controller.startNodeRun(operation, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error {
				<-release
				return nil
			})
			goal := &serving.SchedulerCandidate{Actions: []serving.Action{controller.runs[operation.ID].action}}
			close(release)
			<-done
			lost, failed := controller.failTargetLocked(serving.SchedulerSnapshot{}, goal, time.Now())
			if failed || len(lost) != 0 || result.Status != nativeOperationComplete {
				t.Fatalf("planner relabeled a completed manual operation: lost=%+v failed=%v result=%+v", lost, failed, result)
			}
		})
	}
}

func TestResetScopeResolvesCurrentNonAutonomousWorkloads(t *testing.T) {
	h := newResetHarness(t)
	cm, document := h.configuration(t)
	settings := h.controller.Settings()
	settings.AutonomousWorkloads = []string{h.foreignUID}
	var err error
	document["settings"], err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["scheduler.json"] = string(raw)
	if _, err := h.kube.CoreV1().ConfigMaps(cm.Namespace).Update(context.Background(), cm, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.controller.mu.Lock()
	h.controller.config.Settings = settings
	// Acceptance must capture current runtime Workloads, not resolve an old snapshot.
	h.controller.state.Snapshot.Workloads = []*serving.Definition{{UID: "stale-workload"}}
	h.controller.mu.Unlock()
	request := h.request()
	request.WorkloadUIDs, request.Scope = nil, nonAutonomousResetScope
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := h.controller.ExecuteManual(ctx, request)
	if err != nil || result.Status != nativeOperationComplete || !slices.Equal(result.Operation.WorkloadUIDs, []string{h.selectedUID}) {
		t.Fatalf("scope was not frozen from current non-autonomous Workloads: %+v err=%v", result, err)
	}
	if _, err := h.kube.CoreV1().Pods(h.foreign.Namespace).Get(ctx, h.foreign.Name, meta.GetOptions{}); err != nil {
		t.Fatal("scope included an autonomous foreign owner", err)
	}
}

func TestResetRefusesInvalidScopeBeforeAnyMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*ManualOperation)
	}{
		{"missing scope", func(r *ManualOperation) { r.WorkloadUIDs = nil }},
		{"empty list", func(r *ManualOperation) { r.WorkloadUIDs = []string{} }},
		{"both selectors", func(r *ManualOperation) { r.Scope = nonAutonomousResetScope }},
		{"unknown scope", func(r *ManualOperation) { r.WorkloadUIDs = nil; r.Scope = "all" }},
		{"unknown UID", func(r *ManualOperation) { r.WorkloadUIDs = []string{"absent"} }},
		{"empty UID", func(r *ManualOperation) { r.WorkloadUIDs = []string{""} }},
		{"duplicate UID", func(r *ManualOperation) { r.WorkloadUIDs = append(r.WorkloadUIDs, r.WorkloadUIDs[0]) }},
		{"missing reason", func(r *ManualOperation) { r.Reason = " \t" }},
		{"single UID field", func(r *ManualOperation) { r.WorkloadUID = r.WorkloadUIDs[0] }},
		{"node field", func(r *ManualOperation) { r.Node = testNode }},
		{"weights field", func(r *ManualOperation) { r.Weights = map[string]int32{"p/c": 1} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newResetHarness(t)
			request := h.request()
			tc.alter(&request)
			start := len(h.kube.Actions())
			result, err := h.controller.ExecuteManual(context.Background(), request)
			if errorCode(err) != "InvalidInput" || result.Operation.ID != "" {
				t.Fatalf("malformed reset did not return request refusal: %+v err=%v", result, err)
			}
			if h.api.podWrites != 0 || h.updates != 0 {
				t.Errorf("refused request mutated Pod/configuration: %d/%d", h.api.podWrites, h.updates)
			}
			for _, action := range h.kube.Actions()[start:] {
				if action.GetVerb() != getVerb && action.GetVerb() != "list" {
					t.Errorf("refused request made physical mutation: %s %s", action.GetVerb(), action.GetResource().Resource)
				}
			}
		})
	}
}

func TestResetRefusesConcurrentResetBeforeMutation(t *testing.T) {
	h := newResetHarness(t)
	release := make(chan struct{})
	operation := Operation{ID: "in-flight-reset", Kind: operationReset, WorkloadUIDs: []string{h.selectedUID}}
	done, _ := h.controller.startNodeRun(operation, h.controller.config, func(context.Context, *OperationResult) error {
		<-release
		return nil
	})
	defer func() { close(release); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := h.controller.ExecuteManual(ctx, h.request())
	if err == nil || !slices.Contains([]string{testNodeBusy, testOperationConflict}, errorCode(err)) || result.Operation.ID != "" {
		t.Fatalf("reset admitted while another reset owns its scope: %+v err=%v", result, err)
	}
	if h.api.podWrites != 0 || h.updates != 0 {
		t.Errorf("busy refusal mutated scope: writes=%d updates=%d", h.api.podWrites, h.updates)
	}
}

func TestResetRefusesMeasurementOwnedNodeBeforeMutation(t *testing.T) {
	h := newResetHarness(t)
	node, err := h.kube.CoreV1().Nodes().Get(context.Background(), testNode.Name, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "foreign-measurement", Effect: core.TaintEffectNoSchedule}}
	if _, err := h.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := h.controller.ExecuteManual(context.Background(), h.request())
	if errorCode(err) != testNodeBusy || result.Operation.ID != "" {
		t.Fatalf("reset did not refuse external measurement ownership: %+v err=%v", result, err)
	}
	if h.api.podWrites != 0 || h.updates != 0 {
		t.Errorf("measurement refusal mutated scope: writes=%d updates=%d", h.api.podWrites, h.updates)
	}
}

func TestResetDoesNotTreatForeignBindingAsOwnership(t *testing.T) {
	h := newResetHarness(t)
	current := &core.Pod{}
	if err := h.api.Get(context.Background(), client.ObjectKeyFromObject(h.foreign), current); err != nil {
		t.Fatal(err)
	}
	before := current.DeepCopy()
	current.Annotations[ServingRoutingAnnotation] = `{"` + h.selectedUID + `":1}`
	if err := h.api.Patch(context.Background(), current, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := h.controller.ExecuteManual(ctx, h.request())
	if err != nil && errorCode(err) != testOperationConflict {
		t.Fatalf("foreign ownership produced a non-domain refusal: %+v err=%v", result, err)
	}
	stored, getErr := h.kube.CoreV1().Pods(h.foreign.Namespace).Get(ctx, h.foreign.Name, meta.GetOptions{})
	if getErr != nil || !reflect.DeepEqual(stored.OwnerReferences, h.foreign.OwnerReferences) {
		t.Fatalf("binding was treated as ownership: %+v err=%v result=%+v", stored, getErr, result)
	}
	if err == nil && (result.Status == nativeOperationComplete || result.Error == "" || !strings.Contains(strings.ToLower(result.Error), "owner")) {
		t.Errorf("foreign ownership conflict did not fail with evidence: %+v", result)
	}
}

func TestResetCleansAllDuplicateSlotExecutorsAC36(t *testing.T) {
	controller, prepared := tickReadySubsetController(t, nil)
	prepared.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	kube := controller.kube.(*kubefake.Clientset)
	api := &resetAPI{Client: &nodeAPI{Client: controller.store.client, kube: kube}, kube: kube, terminateOnDelete: true}
	controller.store.client = api
	selected := controller.runtime.SchedulerInputs(time.Now).Workloads[0]
	foreign := *selected
	foreign.Name, foreign.UID = "foreign", "foreign-workload"
	if err := controller.runtime.Publish(&foreign); err != nil {
		t.Fatal(err)
	}
	workload := &edge.Workload{ObjectMeta: meta.ObjectMeta{Name: foreign.Name, UID: types.UID(foreign.UID)}}
	if err := api.Create(ctx, workload); err != nil {
		t.Fatal(err)
	}
	pods := []*core.Pod{}
	for _, name := range []string{"a", "b"} {
		pod := &core.Pod{}
		if err := api.Get(ctx, client.ObjectKey{Namespace: "chill-system", Name: name}, pod); err != nil {
			t.Fatal(err)
		}
		before := pod.DeepCopy()
		pod.Annotations[ServingSlotAnnotation] = "shared-slot"
		if name == "b" {
			pod.Annotations[ServingSlotAnnotation] = physicalID(pods[0].UID, firstfloorStatus(pods[0]).ContainerID)
		}
		if err := api.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
			t.Fatal(err)
		}
		pods = append(pods, pod)
	}
	other := pods[0].DeepCopy()
	other.Name, other.UID, other.ResourceVersion = "c", "foreign-pod", ""
	other.Labels[instancePodUIDLabel] = string(other.UID)
	other.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", Name: foreign.Name, UID: workload.UID}}
	other.Annotations[ServingRoutingAnnotation] = `{"` + foreign.UID + `":1000}`
	if err := api.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := kube.Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), other.DeepCopy(), other.Namespace); err != nil {
		t.Fatal(err)
	}
	service := observerServiceFixture(other)
	service.Spec.ClusterIP = "10.96.0.12"
	if _, err := kube.CoreV1().Services(other.Namespace).Create(ctx, service, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cm := &core.ConfigMap{}
	if err := api.Get(ctx, client.ObjectKeyFromObject(pods[0]), cm); err != nil {
		t.Fatal(err)
	}
	cm.Name, cm.UID, cm.ResourceVersion = other.Name, "", ""
	if err := api.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	id := physicalID(other.UID, firstfloorStatus(other).ContainerID)
	follow, stop := context.WithCancel(controller.observer.ctx)
	defer stop()
	controller.observer.sources[string(other.UID)] = &nativeSource{pod: types.NamespacedName{Namespace: other.Namespace, Name: other.Name},
		uid: other.UID, containerID: firstfloorStatus(other).ContainerID, follow: follow, cancel: stop}
	controller.config.StaticRouting = true
	controller.config.ConfigurationName, controller.config.ConfigurationKey = "duplicate-reset-config", "scheduler.json"
	controller.config.Settings.AutonomousWorkloads = []string{selected.UID, foreign.UID}
	raw, err := json.Marshal(map[string]any{"settings": controller.config.Settings,
		"staticRouting": map[string]map[string]int{foreign.UID: {id: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoreV1().ConfigMaps("chill-system").Create(ctx, &core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Name: controller.config.ConfigurationName, Namespace: "chill-system"},
		Data:       map[string]string{controller.config.ConfigurationKey: string(raw)},
	}, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.capture(ctx, time.Now(), controller.config)
	if err != nil || len(snapshot.Instances) != 3 {
		t.Fatalf("duplicate physical sources not visible: %+v %v", snapshot.Instances, err)
	}
	controller.state.Snapshot = snapshot
	aID := physicalID(pods[0].UID, firstfloorStatus(pods[0]).ContainerID)
	actual, _ := controller.runtime.SchedulerRouting()
	if actual[selected.UID][aID] != 1000 || actual[foreign.UID][id] != 1000 {
		t.Fatalf("fixture did not actually route A and foreign C: %v", actual)
	}
	foreignBefore := other.DeepCopy()
	request := ManualOperation{Kind: operationReset, WorkloadUIDs: []string{selected.UID}, Reason: "clean duplicate slot"}
	first, err := controller.ExecuteManual(ctx, request)
	assertResetComplete(t, first, err)
	if len(first.DrainNative) != 2 {
		t.Fatalf("reset did not read native drain proof for both physical executors: %+v", first.DrainNative)
	}
	for _, pod := range pods {
		for _, object := range []client.Object{&core.Pod{}, &core.ConfigMap{}} {
			if err := api.Get(ctx, client.ObjectKeyFromObject(pod), object); !apierrors.IsNotFound(err) {
				t.Fatalf("owned resource remains (%T): %v", object, err)
			}
		}
		if _, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, instanceService(pod).Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("duplicate executor Service survives reset: %v", err)
		}
	}
	foreignAfter, err := kube.CoreV1().Pods(other.Namespace).Get(ctx, other.Name, meta.GetOptions{})
	if err != nil || !reflect.DeepEqual(foreignBefore, foreignAfter) {
		t.Fatalf("foreign physical ownership/allocation/routing was modified: %+v %v", foreignAfter, err)
	}
	actual, _ = controller.runtime.SchedulerRouting()
	if actual[foreign.UID][id] != 1000 {
		t.Fatalf("foreign distribution changed: %v", actual)
	}
	writes := api.podWrites
	second, err := controller.ExecuteManual(ctx, request)
	assertResetComplete(t, second, err)
	if api.podWrites != writes {
		t.Fatalf("second reset wrote Pods: %d→%d", writes, api.podWrites)
	}
}

func assertResetComplete(t *testing.T, result OperationResult, err error) {
	t.Helper()
	if err != nil || result.Status != nativeOperationComplete {
		t.Fatalf("reset failed: status=%s error=%s err=%v", result.Status, result.Error, err)
	}
}

func TestResetRemovesInitFailedPodByUIDAC37(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			h := newResetHarness(t)
			ctx := context.Background()
			pod := &core.Pod{}
			if err := h.api.Get(ctx, client.ObjectKeyFromObject(h.owned), pod); err != nil {
				t.Fatal(err)
			}
			pod.Status.ContainerStatuses = []core.ContainerStatus{{Name: firstfloorContainerName,
				State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "PodInitializing"}}}}
			pod.Status.InitContainerStatuses = []core.ContainerStatus{{Name: "artifact",
				State: core.ContainerState{Terminated: &core.ContainerStateTerminated{ExitCode: 1}}}}
			pod.Status.Phase = core.PodPending
			if terminal {
				pod.Status.Phase = core.PodFailed
			}
			if err := h.api.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			if err := h.api.syncPod(ctx, pod); err != nil {
				t.Fatal(err)
			}
			h.controller.observer.mu.Lock()
			h.controller.observer.restoreSources([]core.Pod{*pod})
			if err := h.controller.observer.publishLedger(nil); err != nil {
				t.Fatal(err)
			}
			h.controller.observer.mu.Unlock()
			result, err := h.controller.ExecuteManual(ctx, h.request())
			if !terminal {
				if err == nil || h.api.deleteCalls != 0 {
					t.Fatalf("nonterminal Pod without executor was removed: %+v %v", result, err)
				}
				return
			}
			assertResetComplete(t, result, err)
			if err := h.api.Get(ctx, client.ObjectKeyFromObject(pod), &core.Pod{}); !apierrors.IsNotFound(err) {
				t.Fatalf("init-failed Pod still exists: %v", err)
			}
			for _, state := range h.controller.runtime.Instances() {
				if state.PodUID == string(pod.UID) {
					t.Fatalf("removed init-failed Pod retained inventory: %+v", state)
				}
			}
		})
	}
}
