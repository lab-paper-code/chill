package controller

import (
	"context"
	"errors"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
)

const completedPodStage = "stage"

func TestProcedurePodsCollectedAfterResultStatus(t *testing.T) {
	for _, scopeName := range []string{"run", completedPodStage} {
		for _, phase := range []corev1.PodPhase{corev1.PodSucceeded, corev1.PodFailed, corev1.PodRunning} {
			for _, failStatus := range []bool{false, true} {
				name := scopeName + "/" + string(phase)
				if failStatus {
					name += "/status-retry"
				}
				t.Run(name, func(t *testing.T) {
					ctx := context.Background()
					run := fixtureRun("collected")
					startRun(run)
					now := metav1.NewTime(fixtureTime)
					r, store := fixtureRuntime(t, run)
					owner := client.Object(run)
					step, stageID, stageUID := api.EvaluationStepPublish, "", types.UID("")
					if scopeName == completedPodStage {
						stage := fixtureStage(t, &r, run)
						stage.Finalizers = []string{api.EvaluationStageFinalizer}
						if err := r.Client.Update(ctx, stage); err != nil {
							t.Fatal(err)
						}
						beforeStage := stage.DeepCopy()
						stage.Status.StartedAt = &now
						if err := r.patchStatus(ctx, stage, beforeStage); err != nil {
							t.Fatal(err)
						}
						beforeRun := run.DeepCopy()
						run.Status.CurrentStage = stage.Spec.StageID
						run.Status.ChildRefs = []api.EvaluationChildReference{{StageID: stage.Spec.StageID, EvaluationObjectReference: *ref(stage)}}
						if err := r.patchStatus(ctx, run, beforeRun); err != nil {
							t.Fatal(err)
						}
						owner, step, stageID, stageUID = stage, api.EvaluationStepFactor, stage.Spec.StageID, stage.UID
					} else {
						before := run.DeepCopy()
						run.Status.FinishedAt = &now
						if err := r.patchStatus(ctx, run, before); err != nil {
							t.Fatal(err)
						}
					}
					readProcedure := func() *api.EvaluationProcedure {
						t.Helper()
						saved := owner.DeepCopyObject().(client.Object)
						if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(owner), saved); err != nil {
							t.Fatal(err)
						}
						switch v := saved.(type) {
						case *api.EvaluationRun:
							return findProcedure(v.Status.Procedures, step)
						case *api.EvaluationStage:
							return findProcedure(v.Status.Procedures, step)
						}
						panic("unexpected procedure owner")
					}
					statusErr := errors.New("result status write failed")
					rejectStatus := false
					r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
						Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
							if _, ok := object.(*batchv1.Job); ok {
								object.SetUID(types.UID(object.GetName() + "-uid"))
							}
							return c.Create(ctx, object, opts...)
						},
						SubResourcePatch: func(ctx context.Context, c client.Client, sub string, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if rejectStatus {
								return statusErr
							}
							return c.SubResource(sub).Patch(ctx, object, patch, opts...)
						},
						Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
							if _, ok := object.(*corev1.Pod); ok {
								p := readProcedure()
								if p == nil || p.EvidenceRef == nil || p.FinishedAt == nil {
									t.Fatal("Pod deleted before result status persisted")
								}
							}
							return c.Delete(ctx, object, opts...)
						},
					})
					reconcile := func() error {
						request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(owner)}
						if scopeName == completedPodStage {
							_, err := (&StageReconciler{Runtime: r}).Reconcile(ctx, request)
							return err
						}
						_, err := (&RunReconciler{Runtime: r}).Reconcile(ctx, request)
						return err
					}
					if err := reconcile(); err != nil {
						t.Fatal(err)
					}
					p := readProcedure()
					if p == nil || p.JobRef == nil {
						t.Fatal("Job reference was not recorded")
					}
					jobRef := *p.JobRef
					pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "procedure-pod", Namespace: owner.GetNamespace(), UID: "procedure-pod-uid",
						Labels:          map[string]string{"batch.kubernetes.io/controller-uid": string(jobRef.UID)},
						OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: jobRef.Name, UID: jobRef.UID, Controller: ptr(true)}}},
						Status: corev1.PodStatus{Phase: phase}}
					if err := r.Client.Create(ctx, pod); err != nil {
						t.Fatal(err)
					}
					if err := reconcile(); err != nil {
						t.Fatal(err)
					}
					if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); err != nil {
						t.Fatalf("unconsumed Pod must stay: %v", err)
					}
					store.put(evidenceKey(run.UID, scope(stageID)+"/"+step+"/result.json"), map[string]any{
						"schemaVersion": "evaluation-step.v1alpha1", "runUID": run.UID, "stageUID": stageUID, "stageID": stageID, "step": step,
						"execution": map[string]string{"status": complete}, "startedAt": fixtureTime, "finishedAt": fixtureTime})
					if failStatus {
						rejectStatus = true
						if err := reconcile(); !errors.Is(err, statusErr) {
							t.Fatalf("expected status failure, got %v", err)
						}
						if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); err != nil {
							t.Fatalf("failed status write must keep Pod: %v", err)
						}
						rejectStatus = false
					}
					if err := reconcile(); err != nil {
						t.Fatal(err)
					}
					assertCollectedProcedurePod(t, ctx, r.APIReader, pod, phase, client.ObjectKey{Namespace: owner.GetNamespace(), Name: jobRef.Name})
				})
			}
		}
	}
}

func assertCollectedProcedurePod(t *testing.T, ctx context.Context, reader client.Reader, pod *corev1.Pod, phase corev1.PodPhase, jobKey client.ObjectKey) {
	t.Helper()
	podErr := reader.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})
	if phase == corev1.PodSucceeded && !apierrors.IsNotFound(podErr) || phase != corev1.PodSucceeded && podErr != nil {
		t.Fatalf("Pod phase %s: %v", phase, podErr)
	}
	if err := reader.Get(ctx, jobKey, &batchv1.Job{}); err != nil {
		t.Fatalf("consumed Job must stay: %v", err)
	}
}

func TestPastProcedurePodsReconciledByExactJobOwner(t *testing.T) {
	for _, scopeName := range []string{"run", completedPodStage} {
		t.Run(scopeName, func(t *testing.T) {
			ctx := context.Background()
			now := metav1.NewTime(fixtureTime)
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "past-job", Namespace: "system", UID: "past-job-uid"}}
			procedures := []api.EvaluationProcedure{{Step: api.EvaluationStepPrepare, JobRef: ref(job), FinishedAt: &now,
				EvidenceRef: &api.EvaluationEvidenceReference{Store: "evidence", Key: "past-result", Size: 1}}}
			run := fixtureRun("past")
			startRun(run)
			run.Status.FinishedAt = &now
			run.Status.Procedures = procedures
			condition(&run.Status.Conditions, 1, api.EvaluationConditionPublished, metav1.ConditionTrue, complete, "")
			owner := client.Object(run)
			if scopeName == completedPodStage {
				owner = &api.EvaluationStage{ObjectMeta: metav1.ObjectMeta{Name: "past-stage", Namespace: "system", UID: "past-stage-uid"},
					Status: api.EvaluationStageStatus{FinishedAt: &now, Procedures: procedures}}
			}
			objects := []client.Object{owner, job}
			var pods []*corev1.Pod
			for _, name := range []string{"succeeded", "failed", "running", "foreign-uid", "foreign-kind", "foreign-name", "non-controller"} {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "system", UID: types.UID(name + "-uid"),
					Labels:          map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)},
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr(true)}}},
					Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
				switch name {
				case "failed":
					pod.Status.Phase = corev1.PodFailed
				case "running":
					pod.Status.Phase = corev1.PodRunning
				case "foreign-uid":
					pod.OwnerReferences[0].UID = "other-job-uid"
				case "foreign-kind":
					pod.OwnerReferences[0].Kind = "Other"
				case "foreign-name":
					pod.OwnerReferences[0].Name = "other-job"
				case "non-controller":
					pod.OwnerReferences[0].Controller = ptr(false)
				}
				pods = append(pods, pod)
				objects = append(objects, pod)
			}
			r, _ := fixtureRuntime(t, objects...)
			for range 2 {
				request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(owner)}
				var err error
				if scopeName == completedPodStage {
					_, err = (&StageReconciler{Runtime: r}).Reconcile(ctx, request)
				} else {
					_, err = (&RunReconciler{Runtime: r}).Reconcile(ctx, request)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, pod := range pods {
				err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{})
				if pod.Name == "succeeded" && !apierrors.IsNotFound(err) || pod.Name != "succeeded" && err != nil {
					t.Fatalf("Pod %s: %v", pod.Name, err)
				}
			}
			if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); err != nil {
				t.Fatalf("past Job must stay: %v", err)
			}
		})
	}
}
