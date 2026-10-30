package kube

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func manualConfigurationFixture(t *testing.T) (*SchedulerController, *kubefake.Clientset) {
	t.Helper()
	settings := serving.SchedulerSettings{
		ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second, MaxObservationAge: time.Second,
		MaxReplicas: 1, MaxInstances: 1, MaxCPUs: 1, CapacityWindow: time.Second, CapacityWindows: 1,
		CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
		CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1, AutonomousWorkloads: []string{"auto", "keep-auto"},
	}
	raw, err := json.Marshal(map[string]any{
		"settings": settings, "staticRouting": map[string]map[string]int{"w": {"old": 3}, "other": {"physical": 7}},
		"reserved": map[string]any{"node": map[string]any{"cpus": []int{2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	kube := kubefake.NewSimpleClientset(&core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "42"}, Data: map[string]string{
		"other-key": "untouched", "scheduler.json": string(raw),
	}})
	return &SchedulerController{kube: kube, operations: &Operations{config: OperationConfig{Namespace: "serving"}}, config: SchedulerControllerConfig{Settings: settings, ConfigurationName: "scheduler", ConfigurationKey: "scheduler.json"}}, kube
}

func TestManualRoutingPersistencePreservesOtherUIDsAndConfiguration(t *testing.T) {
	for _, kind := range []string{operationRoute, operationUnroute, operationRelease, operationReset} {
		t.Run(kind, func(t *testing.T) {
			controller, kube := manualConfigurationFixture(t)
			operation := Operation{Kind: kind, WorkloadUID: "w", WorkloadUIDs: []string{"auto"}, Reason: "operator choice", ActorUsername: "alice", ActorUID: "actor", Target: &serving.SchedulerCandidate{Routing: map[string]map[string]int32{"w": {"new": 11}}}}
			if kind == operationUnroute {
				operation.Target.Routing["w"] = map[string]int32{}
			}
			if err := controller.persistManualConfiguration(context.Background(), &operation); err != nil {
				t.Fatal(err)
			}
			cm, err := kube.CoreV1().ConfigMaps("serving").Get(context.Background(), "scheduler", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal([]byte(cm.Data["scheduler.json"]), &document); err != nil {
				t.Fatal(err)
			}
			if cm.Data["other-key"] != "untouched" || string(document["reserved"]) != `{"node":{"cpus":[2]}}` {
				t.Fatalf("unrelated configuration changed: %v", cm.Data)
			}
			if operation.SourceConfigRV != cm.ResourceVersion || operation.Reason != "operator choice" || operation.ActorUsername != "alice" || operation.ActorUID != "actor" {
				t.Fatalf("provenance lost: %+v", operation)
			}
			if !maps.Equal(operation.BeforeStaticRouting["w"], map[string]int{"old": 3}) || !maps.Equal(operation.StaticRouting["other"], map[string]int{"physical": 7}) {
				t.Fatalf("before/other declarations lost: %+v", operation)
			}
			switch kind {
			case operationRoute:
				if !maps.Equal(operation.StaticRouting["w"], map[string]int{"new": 11}) {
					t.Fatal(operation.StaticRouting)
				}
			case operationUnroute:
				if weights, ok := operation.StaticRouting["w"]; !ok || len(weights) != 0 {
					t.Fatal("unroute must retain an explicit empty declaration")
				}
			case operationRelease:
				if _, ok := operation.StaticRouting["w"]; ok {
					t.Fatal("release retained UID declaration")
				}
			case operationReset:
				if !maps.Equal(operation.StaticRouting["w"], map[string]int{"old": 3}) {
					t.Fatal("reset step1 changed routing")
				}
				if got := controller.Settings().AutonomousWorkloads; len(got) != 1 || got[0] != "keep-auto" {
					t.Fatalf("reset changed other autonomy: %v", got)
				}
			}
		})
	}
}

func TestManualRoutingPersistenceConflictDoesNotRetryOrAccept(t *testing.T) {
	controller, kube := manualConfigurationFixture(t)
	failure := errors.New("resourceVersion conflict")
	updates := 0
	kube.PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) { updates++; return true, nil, failure })
	operation := Operation{Kind: operationRelease, WorkloadUID: "w"}
	if err := controller.persistManualConfiguration(context.Background(), &operation); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if updates != 1 || operation.SourceConfigRV != "" || controller.config.StaticRouting {
		t.Fatalf("failed declaration accepted/retried: %+v updates=%d", operation, updates)
	}
}

func TestManualRoutingAdmissionRefusesBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name       string
		request    ManualOperation
		autonomous bool
		running    bool
	}{
		{name: "missing reason", request: ManualOperation{Kind: operationUnroute, WorkloadUID: "w"}},
		{name: "stale UID", request: ManualOperation{Kind: operationUnroute, WorkloadUID: "stale", Reason: "reason"}},
		{name: "autonomous", request: ManualOperation{Kind: operationUnroute, WorkloadUID: "w", Reason: "reason"}, autonomous: true},
		{name: "reserved workload", request: ManualOperation{Kind: operationRelease, WorkloadUID: "w", Reason: "reason"}, running: true},
		{name: "zero route", request: ManualOperation{Kind: operationRoute, WorkloadUID: "w", Reason: "reason", Weights: map[string]int32{"physical": 0}}},
		{name: "negative route", request: ManualOperation{Kind: operationRoute, WorkloadUID: "w", Reason: "reason", Weights: map[string]int32{"physical": -1}}},
		{name: "unroute weights", request: ManualOperation{Kind: operationUnroute, WorkloadUID: "w", Reason: "reason", Weights: map[string]int32{}}},
		{name: "release weights", request: ManualOperation{Kind: operationRelease, WorkloadUID: "w", Reason: "reason", Weights: map[string]int32{}}},
		{name: "missing target", request: ManualOperation{Kind: operationRoute, WorkloadUID: "w", Reason: "reason", Weights: map[string]int32{"missing": 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &SchedulerController{runs: map[string]*actionRun{}}
			config := SchedulerControllerConfig{}
			if test.autonomous {
				config.Settings.AutonomousWorkloads = []string{"w"}
			}
			if test.running {
				controller.runs["route"] = &actionRun{action: serving.Action{Kind: operationRoute, Status: nativeOperationRunning}, reserved: true, owner: "route", scope: resourceScope{Workloads: []string{"w"}}}
			}
			_, err := controller.manualRoutingOperationLocked(context.Background(), test.request, config, serving.SchedulerSnapshot{Workloads: []*serving.Definition{{UID: "w"}}})
			if err == nil {
				t.Fatal("unsafe operation admitted")
			}
		})
	}
}

func TestManualRoutingDeclarationIsNotPhysicalSuccess(t *testing.T) {
	controller, kube := manualConfigurationFixture(t)
	manager, _ := newManagerHarness(t)
	controller.runtime = manager.runtime
	controller.operations.runtime = manager.runtime
	operation := Operation{
		Kind: operationRoute, WorkloadUID: "w", Reason: "operator choice",
		Target: &serving.SchedulerCandidate{
			Routing:   map[string]map[string]int32{"w": {"missing": 1}},
			Instances: []serving.SchedulerInstance{{ID: "missing", WorkloadUID: "w"}},
		},
	}
	result, err := controller.executeManualRouting(context.Background(), operation)
	if err == nil || result.Status != nativeOperationPartial || result.Error == "" {
		t.Fatalf("declaration was treated as success: %+v %v", result, err)
	}
	cm, err := kube.CoreV1().ConfigMaps("serving").Get(context.Background(), "scheduler", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		StaticRouting map[string]map[string]int `json:"staticRouting"`
	}
	if err := json.Unmarshal([]byte(cm.Data["scheduler.json"]), &document); err != nil {
		t.Fatal(err)
	}
	if document.StaticRouting["w"]["missing"] != 1 || result.Actual.Routing["w"]["missing"] != 0 {
		t.Fatalf("declaration and actual routing conflated: %+v %+v", document, result)
	}
}

func manualAdmissionController(t *testing.T) *SchedulerController {
	t.Helper()
	controller, prepared := tickReadySubsetController(t, nil)
	prepared.Store(true)
	kube := controller.kube.(*kubefake.Clientset)
	policies := &core.ConfigMapList{}
	if err := controller.store.client.List(context.Background(), policies); err != nil {
		t.Fatal(err)
	}
	for _, policy := range policies.Items {
		if _, err := kube.CoreV1().ConfigMaps(policy.Namespace).Create(context.Background(), policy.DeepCopy(), meta.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	controller.store.client = &resetAPI{
		Client: &nodeAPI{Client: controller.store.client, kube: kube}, kube: kube, terminateOnDelete: true,
	}
	controller.config.Settings.AutonomousWorkloads = nil
	controller.config.ConfigurationName, controller.config.ConfigurationKey = "manual-admission", "scheduler.json"
	raw, err := json.Marshal(map[string]any{"settings": controller.config.Settings})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoreV1().ConfigMaps(controller.operations.config.Namespace).Create(context.Background(), &core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Name: controller.config.ConfigurationName, Namespace: controller.operations.config.Namespace},
		Data:       map[string]string{controller.config.ConfigurationKey: string(raw)},
	}, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return controller
}

func TestManualUnrouteACKAllowsRemoveBeforeNextTick(t *testing.T) {
	controller := manualAdmissionController(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := controller.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot := controller.State().Snapshot
	instance := snapshot.Instances[0]
	uid := snapshot.Workloads[0].UID
	if snapshot.Routing[uid][instance.ID] <= 0 {
		t.Fatal("last Tick must observe a routed instance")
	}
	unroute, err := controller.ExecuteManual(ctx, ManualOperation{Kind: operationUnroute, WorkloadUID: uid, Reason: "withdraw before removal"})
	if err != nil || unroute.Status != nativeOperationComplete || len(unroute.Actual.Routing[uid]) != 0 {
		t.Fatalf("unroute did not ACK: %+v err=%v", unroute, err)
	}
	if controller.State().Snapshot.Routing[uid][instance.ID] <= 0 {
		t.Fatal("test must retain the pre-ACK Tick snapshot")
	}
	remove, err := controller.ExecuteManual(ctx, ManualOperation{Kind: operationRemove, WorkloadUID: uid, Node: instance.Node, Instance: instance.ID, Reason: "remove after unroute ACK"})
	if err != nil || remove.Status != nativeOperationComplete {
		t.Fatalf("ACKed unroute still required another Tick before remove: %+v err=%v", remove, err)
	}
}

func TestManualRouteACKPreservesOtherWorkloadBeforeNextTick(t *testing.T) {
	controller := manualAdmissionController(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := controller.runtime.SchedulerInputs(time.Now).Workloads[0]
	second := *first
	second.Name, second.UID = "second-workload", "second-workload-uid"
	if err := controller.runtime.Publish(&second); err != nil {
		t.Fatal(err)
	}
	pod, err := controller.kube.CoreV1().Pods(controller.operations.config.Namespace).Get(ctx, "b", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.OwnerReferences[0].Name, pod.OwnerReferences[0].UID = second.Name, "second-workload-uid"
	if err := controller.store.client.Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.kube.CoreV1().Pods(pod.Namespace).Update(ctx, pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	snapshot := controller.State().Snapshot
	a, b := snapshot.Instances[0], snapshot.Instances[1]
	for _, request := range []ManualOperation{
		{Kind: operationRoute, WorkloadUID: first.UID, Weights: map[string]int32{a.ID: 17}, Reason: "route first Workload"},
		{Kind: operationRoute, WorkloadUID: second.UID, Weights: map[string]int32{b.ID: 23}, Reason: "route second Workload after first ACK"},
	} {
		result, err := controller.ExecuteManual(ctx, request)
		if err != nil || result.Status != nativeOperationComplete {
			t.Fatalf("sequential route required another Tick: %+v err=%v", result, err)
		}
	}
	actual, _ := controller.runtime.SchedulerRouting()
	if !maps.Equal(actual[first.UID], map[string]int32{a.ID: 17}) || !maps.Equal(actual[second.UID], map[string]int32{b.ID: 23}) {
		t.Fatalf("ACKed routes were not both preserved: %v", actual)
	}
	if controller.State().Snapshot.Routing[first.UID][a.ID] == 17 {
		t.Fatal("test must retain the pre-ACK Tick snapshot")
	}
}
