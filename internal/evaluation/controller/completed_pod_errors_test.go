package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestProcedurePodGCErrorDoesNotBlockReconcile(t *testing.T) {
	for _, scopeName := range []string{"run-status", "run-finalizer", "stage-status", "stage-finalizer"} {
		for _, operation := range []string{"list", "delete"} {
			t.Run(scopeName+"/"+operation, func(t *testing.T) {
				ctx := context.Background()
				now := metav1.NewTime(fixtureTime)
				procedures := []api.EvaluationProcedure{{Step: api.EvaluationStepPrepare,
					JobRef: &api.EvaluationObjectReference{Name: "past-job", UID: "past-job-uid"}, FinishedAt: &now,
					EvidenceRef: &api.EvaluationEvidenceReference{Store: "evidence", Key: "past-result", Size: 1}}}
				run := fixtureRun("cleanup-error")
				run.Status.Procedures = procedures
				owner := client.Object(run)
				finalizer := api.EvaluationRunFinalizer
				switch scopeName {
				case "run-status":
					startRun(run)
					run.Status.FinishedAt = &now
				case "run-finalizer":
					run.Status.FinishedAt = &now
					startRun(run)
					run.DeletionTimestamp = &now
					condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionCleanupComplete, metav1.ConditionTrue, complete, "")
					publish := procedures[0]
					publish.Step = api.EvaluationStepPublish
					run.Status.Procedures = append(run.Status.Procedures, publish)
				case "stage-status", "stage-finalizer":
					stage := &api.EvaluationStage{ObjectMeta: metav1.ObjectMeta{Name: "stage", Namespace: run.Namespace, UID: "stage-uid", Finalizers: []string{api.EvaluationStageFinalizer}},
						Status: api.EvaluationStageStatus{Procedures: procedures}}
					if scopeName == "stage-finalizer" {
						stage.Status.FinishedAt = &now
						stage.DeletionTimestamp = &now
					}
					owner, finalizer = stage, api.EvaluationStageFinalizer
				}
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "succeeded", Namespace: owner.GetNamespace(), UID: "pod-uid",
					Labels:          map[string]string{"batch.kubernetes.io/controller-uid": "past-job-uid"},
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "past-job", UID: "past-job-uid", Controller: ptr(true)}}},
					Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
				r, _ := fixtureRuntime(t, owner, pod)
				failures := 0
				gcErr := errors.New("Pod collection unavailable")
				c := interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*corev1.PodList); ok && operation == "list" {
							failures++
							return gcErr
						}
						return c.List(ctx, list, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
						if _, ok := object.(*corev1.Pod); ok && operation == "delete" {
							failures++
							return gcErr
						}
						return c.Delete(ctx, object, opts...)
					},
				})
				r.Client, r.APIReader = c, c
				request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(owner)}
				var result ctrl.Result
				var err error
				if _, ok := owner.(*api.EvaluationStage); ok {
					result, err = (&StageReconciler{Runtime: r}).Reconcile(ctx, request)
				} else {
					result, err = (&RunReconciler{Runtime: r}).Reconcile(ctx, request)
				}
				if err != nil || result.RequeueAfter == 0 || failures != 1 {
					t.Fatalf("GC failure must only schedule retry: result=%v err=%v failures=%d", result, err, failures)
				}
				saved := owner.DeepCopyObject().(client.Object)
				if err := r.APIReader.Get(ctx, request.NamespacedName, saved); err != nil {
					if (scopeName == "stage-finalizer" || scopeName == "run-finalizer") && apierrors.IsNotFound(err) {
						return
					}
					t.Fatal(err)
				}
				switch scopeName {
				case "run-status":
					publish := findProcedure(saved.(*api.EvaluationRun).Status.Procedures, api.EvaluationStepPublish)
					if publish == nil || publish.JobRef == nil {
						t.Fatal("Pod GC failure blocked Run publication")
					}
				case "stage-status":
					if saved.(*api.EvaluationStage).Status.FinishedAt == nil {
						t.Fatal("Pod GC failure blocked orphan Stage status completion")
					}
				default:
					if controllerutil.ContainsFinalizer(saved, finalizer) {
						t.Fatal("Pod GC failure blocked finalizer removal")
					}
				}
			})
		}
	}
}
