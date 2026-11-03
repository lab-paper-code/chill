package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestConditionReasonBoundary(t *testing.T) {
	for _, reason := range []string{"HTTP Error 403", "", "Bad-Reason", "Trailing:", strings.Repeat("A", 1025), "Valid_,:Reason", "A"} {
		var conditions []metav1.Condition
		condition(&conditions, 1, "ExecutionComplete", metav1.ConditionFalse, reason, strings.Repeat("界", 20000))
		c := conditions[0]
		if errs := metav1validation.ValidateConditions(conditions, field.NewPath("conditions")); len(errs) > 0 {
			t.Fatal(errs)
		}
		valid := len(reason) <= 1024 && conditionReason.MatchString(reason)
		if valid && c.Reason != reason || !valid && c.Reason != api.EvaluationReasonProcedureFailed {
			t.Fatalf("reason %q: %+v", reason, c)
		}
		if !valid && !strings.HasPrefix(c.Message, reason+": ") {
			t.Fatalf("original reason missing: %+v", c)
		}
	}
}

func TestInvalidHarnessReasonReturnsPool(t *testing.T) {
	ctx := context.Background()
	parent := fixtureRun("invalid-reason")
	r, store := fixtureRuntime(t, parent, fixtureConfig())
	base := r.Client
	r.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			o.SetUID(types.UID(o.GetName() + "-uid"))
			if err := c.Create(ctx, o, opts...); err != nil {
				return err
			}
			job, ok := o.(*batchv1.Job)
			if !ok {
				return nil
			}
			step := job.Labels[api.EvaluationLabelStep]
			result := map[string]any{"schemaVersion": "evaluation-step.v1alpha1", "runUID": parent.UID, "step": step, "execution": map[string]string{"status": complete, "reason": "ProcedureComplete"}, "startedAt": fixtureTime, "finishedAt": fixtureTime}
			if step == api.EvaluationStepPrepare {
				result["execution"] = map[string]string{"status": failed, "reason": "HTTP Error 403"}
			}
			if step == api.EvaluationStepFinish {
				result["cleanup"] = map[string]string{"status": complete, "reason": "NoControlEffects"}
			}
			store.put(evidenceKey(parent.UID, "run/"+step+"/result.json"), result)
			return nil
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			if run, ok := o.(*api.EvaluationRun); ok {
				if errs := metav1validation.ValidateConditions(run.Status.Conditions, field.NewPath("status", "conditions")); len(errs) > 0 {
					return fmt.Errorf("invalid status: %v", errs)
				}
			}
			return c.SubResource(sub).Patch(ctx, o, p, opts...)
		},
	})
	reconciler := RunReconciler{Runtime: r}
	for range 20 {
		if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}); err != nil {
			t.Fatal(err)
		}
		saved := &api.EvaluationRun{}
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(parent), saved); err != nil {
			t.Fatal(err)
		}
		if saved.Status.FinishedAt != nil {
			c := meta.FindStatusCondition(saved.Status.Conditions, api.EvaluationConditionExecutionComplete)
			if c == nil || c.Status != metav1.ConditionFalse || c.Reason != api.EvaluationReasonProcedureFailed || !strings.Contains(c.Message, "HTTP Error 403") {
				t.Fatalf("failure lost: %+v", c)
			}
			if occupied(saved) || saved.Status.OccupyingRunUID != "" || isTrue(saved.Status.Conditions, api.EvaluationConditionPoolAcquired) || len(saved.Status.ChildRefs) != 0 {
				t.Fatalf("pool retained or stage started: %+v", saved.Status)
			}
			return
		}
	}
	t.Fatal("failed prepare did not finish")
}
