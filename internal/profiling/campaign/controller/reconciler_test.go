package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDeriveStatusPreservesConditionTimeUntilStatusTransitions(t *testing.T) {
	transition := metav1.NewTime(time.Date(2026, time.August, 11, 1, 2, 3, 0, time.UTC))
	previous := []metav1.Condition{{
		Type: batchEnergyOptimizationConditionDone, Status: metav1.ConditionUnknown,
		LastTransitionTime: transition,
	}}
	status := deriveStatus(
		7,
		batchenergyoptimization.Snapshot{Trials: []batchenergyoptimization.Trial{{Verdict: batchenergyoptimization.VerdictCompleted}}},
		batchenergyoptimization.Decision{Action: batchenergyoptimization.ActionWait, Reason: "TrialInFlight"},
		nil,
		previous,
	)
	if len(status.Conditions) != 1 || !status.Conditions[0].LastTransitionTime.Equal(&transition) {
		t.Fatalf("condition transition time = %v, want %v", status.Conditions, transition)
	}
	if status.CompletedTrials != 1 || status.ObservedGeneration != 7 {
		t.Fatalf("derived status = %#v", status)
	}
}

func TestCompletedRunsRequiresOneRunPerProjectedTrial(t *testing.T) {
	_, _, err := completedRuns("parent", batchenergyoptimization.Snapshot{
		Trials: []batchenergyoptimization.Trial{{Verdict: batchenergyoptimization.VerdictCompleted}},
	}, nil)
	if err == nil {
		t.Fatal("trial/Run positional invariant mismatch was completed")
	}
}

type terminalReplayDependencies struct{ resolves, derives, writes int }

func (d *terminalReplayDependencies) Resolve(context.Context, *api.BatchEnergyOptimization) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error) {
	d.resolves++
	return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, errors.New("resolver still unavailable")
}
func (d *terminalReplayDependencies) Derive(context.Context, profilederivation.CurvePlan, []api.ProfilingRun, []api.ProfilingRun, []int, profilederivation.EnergyObjectivePolicy) (batchenergyoptimization.CurveResult, error) {
	d.derives++
	return batchenergyoptimization.CurveResult{}, errors.New("unexpected derivation after terminal publication")
}
func (d *terminalReplayDependencies) Write(context.Context, *api.BatchEnergyOptimization, batchenergyoptimization.StoredObjectReference, []api.ProfilingRun, batchenergyoptimization.Result) (batchenergyoptimization.StoredObjectReference, error) {
	d.writes++
	return batchenergyoptimization.StoredObjectReference{}, errors.New("unexpected evidence recreation")
}

func TestTerminalCampaignReplayDoesNotRecreateEvidenceButResolverFailureStillRetries(t *testing.T) {
	for _, test := range []struct {
		name, phase, reason string
		status              metav1.ConditionStatus
		terminal            bool
	}{
		{"complete", "Complete", "ResultWritten", metav1.ConditionTrue, true},
		{"exhausted attempts", "Failed", terminalReasonRetryLimitExhausted, metav1.ConditionFalse, true},
		{"failed transition", "Failed", terminalReasonTransitionFailed, metav1.ConditionFalse, true},
		{"recoverable resolver failure", "Failed", "CurvePlanResolutionFailed", metav1.ConditionFalse, false},
		{"in progress", "Measuring", "TrialInFlight", metav1.ConditionUnknown, false},
		{"stale terminal generation", "Complete", "ResultWritten", metav1.ConditionTrue, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			parent := &api.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Namespace: "measurements", Name: "terminal", UID: "native", Generation: 2, Annotations: map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}}, Status: api.BatchEnergyOptimizationStatus{ObservedGeneration: 2, Phase: test.phase, Outcome: test.reason, Result: &api.ProfilingRunEvidenceReference{Store: "evidence", Key: "removed/result.json", Size: 1}, Conditions: []metav1.Condition{{Type: batchEnergyOptimizationConditionDone, Status: test.status, Reason: test.reason, ObservedGeneration: 2}}}}
			if test.name == "stale terminal generation" {
				parent.Status.ObservedGeneration = 1
				parent.Status.Conditions[0].ObservedGeneration = 1
			}
			native := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).WithObjects(parent).Build()
			dependencies := &terminalReplayDependencies{}
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, dependencies, dependencies, dependencies)
			if err != nil {
				t.Fatal(err)
			}
			_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: parent.Namespace, Name: parent.Name}})
			if (err == nil) != test.terminal || (dependencies.resolves == 0) != test.terminal || dependencies.derives != 0 || dependencies.writes != 0 {
				t.Fatalf("terminal=%v err=%v resolves=%d derives=%d writes=%d", test.terminal, err, dependencies.resolves, dependencies.derives, dependencies.writes)
			}
		})
	}
}

func TestCampaignWaitsForAdmission(t *testing.T) {
	for _, test := range []struct {
		name, admission, phase string
		requeue                time.Duration
	}{
		{"not admitted", "", "", 5 * time.Second},
		{"refused", profilingrun.RefusedAdmission("NodeServing", "serving/a"), batchEnergyOptimizationPhaseFailed, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			parent := &api.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Namespace: "measurements", Name: "waiting", Generation: 1, Annotations: map[string]string{profilingrun.AdmissionAnnotation: test.admission}}}
			native := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).WithObjects(parent).Build()
			dependencies := &terminalReplayDependencies{}
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, dependencies, dependencies, dependencies)
			if err != nil {
				t.Fatal(err)
			}
			key := types.NamespacedName{Namespace: parent.Namespace, Name: parent.Name}
			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			if err != nil || result.RequeueAfter != test.requeue || dependencies.resolves != 0 {
				t.Fatalf("result=%#v err=%v resolves=%d", result, err, dependencies.resolves)
			}
			updated := &api.BatchEnergyOptimization{}
			if err := native.Get(context.Background(), key, updated); err != nil {
				t.Fatal(err)
			}
			runs := &api.ProfilingRunList{}
			transitions := &api.NodeActuationList{}
			if err := native.List(context.Background(), runs); err != nil {
				t.Fatal(err)
			}
			if err := native.List(context.Background(), transitions); err != nil {
				t.Fatal(err)
			}
			if updated.Status.Phase != test.phase || len(runs.Items)+len(transitions.Items) != 0 {
				t.Fatalf("status=%#v children=%d", updated.Status, len(runs.Items)+len(transitions.Items))
			}
			if test.phase != "" && (updated.Status.Outcome != "NodeServing" || updated.Status.Conditions[0].Reason != "NodeServing" || updated.Status.Conditions[0].Status != metav1.ConditionFalse) {
				t.Fatalf("refusal status = %#v", updated.Status)
			}
		})
	}
}
