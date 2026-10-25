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
		{name: "global route", request: ManualOperation{Kind: operationRelease, WorkloadUID: "w", Reason: "reason"}, running: true},
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
				controller.runs["route"] = &actionRun{action: serving.Action{Kind: operationRoute, Status: nativeOperationRunning}}
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
