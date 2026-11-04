package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestActiveEvaluationProjection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	native := fake.NewClientBuilder().WithScheme(scheme).Build()
	service := Service{reader: native}
	empty, err := service.ListActive(context.Background())
	if err != nil || len(empty) != 0 {
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
	if err != nil || len(result) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	item := result[0]
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

func TestEvaluationListBatchesStageReads(t *testing.T) {
	for _, count := range []int{1, 20} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := batchv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			var objects []client.Object
			for i := range count {
				run := &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("run-%02d", i), Namespace: "ns"}}
				run.UID = types.UID(run.Name)
				for _, id := range []string{"z", "a"} {
					stage := &api.EvaluationStage{
						ObjectMeta: metav1.ObjectMeta{Name: run.Name + "-" + id, Namespace: "ns"},
						Spec:       api.EvaluationStageSpec{StageID: id},
					}
					stage.UID = types.UID(stage.Name)
					run.Status.ChildRefs = append(run.Status.ChildRefs, api.EvaluationChildReference{
						StageID: id, EvaluationObjectReference: api.EvaluationObjectReference{Name: stage.Name, UID: stage.UID},
					})
					objects = append(objects, stage)
					now := metav1.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
					for _, status := range []string{loadJobActive, loadJobSucceeded, loadJobFailed} {
						job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
							Name: stage.Name + "-" + status, Namespace: "ns", UID: types.UID(stage.Name + status),
							Labels: map[string]string{
								"edge.dacs.io/workload-role": "load-generator",
								api.EvaluationLabelRunUID:    string(run.UID), api.EvaluationLabelStage: id,
							},
							OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(stage, api.GroupVersion.WithKind("EvaluationStage"))},
						}, Status: batchv1.JobStatus{StartTime: &now}}
						switch status {
						case loadJobSucceeded:
							job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: "Completed"}}
							job.Status.CompletionTime = &now
						case loadJobFailed:
							job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}
						default:
							// Failed Pods are not a failed Job until its terminal condition says so.
							job.Status.Failed = 1
							job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionFalse}}
						}
						objects = append(objects, job)
					}
				}
				objects = append(objects, run)
			}
			stageLists, stageGets, jobLists := 0, 0, 0
			native := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*api.EvaluationStageList); ok {
						stageLists++
					}
					if _, ok := list.(*batchv1.JobList); ok {
						jobLists++
					}
					return c.List(ctx, list, opts...)
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*api.EvaluationStage); ok {
						stageGets++
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).Build()
			service := New(native, native, "ns", nil)
			result, err := service.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(result) != count || stageLists != 1 || stageGets != 0 || jobLists != 1 {
				t.Fatalf("runs=%d stageLists=%d stageGets=%d jobLists=%d", len(result), stageLists, stageGets, jobLists)
			}
			for _, run := range result {
				if len(run.Stages) != 2 || run.Stages[0].ID != "z" || run.Stages[1].ID != "a" ||
					run.Stages[0].UID != run.Name+"-z" || run.Stages[1].UID != run.Name+"-a" {
					t.Fatalf("run %s stages=%+v", run.Name, run.Stages)
				}
				for _, stage := range run.Stages {
					assertLoadJobs(t, stage)
				}
			}
			detail, err := service.Get(context.Background(), result[0].Name)
			if err != nil {
				t.Fatal(err)
			}
			if jobLists != 2 {
				t.Fatalf("detail must list its load Jobs once, calls=%d", jobLists-1)
			}
			for _, stage := range detail.Stages {
				assertLoadJobs(t, stage)
			}
		})
	}
}

func assertLoadJobs(t *testing.T, stage EvaluationStage) {
	t.Helper()
	if len(stage.LoadJobs) != 3 {
		t.Fatalf("stage %s jobs=%+v", stage.Name, stage.LoadJobs)
	}
	statuses := make(map[string]bool)
	for _, job := range stage.LoadJobs {
		statuses[job.Status] = true
		if job.Name != stage.Name+"-"+job.Status || job.UID != stage.Name+job.Status || job.StartedAt == nil {
			t.Fatalf("Job identity/timing lost: %+v", job)
		}
		if job.Status == loadJobSucceeded && (job.CompletedAt == nil || job.Reason != "Completed") {
			t.Fatalf("success facts lost: %+v", job)
		}
		if job.Status == loadJobFailed && (job.CompletedAt != nil || job.Reason != "BackoffLimitExceeded") {
			t.Fatalf("failure facts changed: %+v", job)
		}
	}
	if !statuses[loadJobActive] || !statuses[loadJobSucceeded] || !statuses[loadJobFailed] {
		t.Fatalf("Job conditions not projected: %+v", stage.LoadJobs)
	}
}
