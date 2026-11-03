package service

import (
	"context"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestActiveEvaluationProjection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	native := fake.NewClientBuilder().WithScheme(scheme).Build()
	service := Service{reader: native}
	empty, err := service.ListActive(context.Background())
	if err != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	now := metav1.NewTime(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	const stageID = "second"
	run := &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns", UID: "run-uid"}}
	run.Spec.Stages = []api.StageTemplate{{ID: "first"}, {ID: stageID}}
	run.Status.StartedAt = &now
	run.Status.CurrentStage = stageID
	run.Status.Conditions = []metav1.Condition{{Type: api.EvaluationConditionPoolAcquired, Status: metav1.ConditionTrue}}
	run.Status.ChildRefs = []api.EvaluationChildReference{{
		StageID: stageID, EvaluationObjectReference: api.EvaluationObjectReference{Name: "stage", UID: "stage-uid"},
	}}
	stage := &api.EvaluationStage{ObjectMeta: metav1.ObjectMeta{Name: "stage", Namespace: "ns", UID: "stage-uid"}}
	stage.Spec.StageID = stageID
	stage.Spec.LoadScenarioRef = api.EvaluationObjectReference{Name: "load", UID: "load-uid"}
	stage.Spec.FailureInjection = []api.FaultInjectionPlan{
		{ID: "kill", Action: "delete-serving-pod", OffsetSeconds: 12},
		{ID: "pending", Action: "delete-serving-pod", OffsetSeconds: 30},
	}
	stage.Status.StartedAt = &now
	stage.Status.GoAt = &now
	stage.Status.InjectionRecords = []api.FaultInjectionRecord{{
		ID: "kill", Outcome: "Executed", NodeRef: &api.EvaluationObjectReference{Name: "node"},
		PodRef: &api.EvaluationObjectReference{Name: "pod"},
	}}
	load := &api.LoadScenario{ObjectMeta: metav1.ObjectMeta{Name: "load", Namespace: "ns", UID: "load-uid"}}
	load.Spec.Parts = []api.LoadScenarioPart{{DurationSeconds: 120}, {DurationSeconds: 180}}
	for _, object := range []client.Object{run, stage, load} {
		if err := native.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.ListActive(context.Background())
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	item := result.Items[0]
	if item.Name != "run" || item.UID != "run-uid" || item.StageIndex != 2 || item.StagesTotal != 2 ||
		!item.StartedAt.Equal(&now) {
		t.Fatalf("run=%+v", item)
	}
	projected := item.Stage
	if projected == nil || projected.ID != stageID ||
		projected.PlannedSeconds != 300 || !projected.GoAt.Equal(&now) {
		t.Fatalf("stage=%+v", projected)
	}
	if len(projected.Injections) != 2 {
		t.Fatalf("injections=%+v", projected.Injections)
	}
	injection := projected.Injections[0]
	if injection.ID != "kill" || injection.Action != "delete-serving-pod" || injection.OffsetSeconds != 12 ||
		injection.Outcome != "Executed" || injection.NodeName != "node" || injection.PodName != "pod" {
		t.Fatalf("injection=%+v", injection)
	}
	if projected.Injections[1].Outcome != "" {
		t.Fatal("pending injection has outcome")
	}
}
