package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
		{"energy objective policy mismatch", "Failed", terminalReasonEnergyObjectivePolicyMismatch, metav1.ConditionFalse, true},
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
			if err := native.List(context.Background(), runs); err != nil {
				t.Fatal(err)
			}
			if updated.Status.Phase != test.phase || len(runs.Items) != 0 {
				t.Fatalf("status=%#v children=%d", updated.Status, len(runs.Items))
			}
			if test.phase != "" && (updated.Status.Outcome != "NodeServing" || updated.Status.Conditions[0].Reason != "NodeServing" || updated.Status.Conditions[0].Status != metav1.ConditionFalse) {
				t.Fatalf("refusal status = %#v", updated.Status)
			}
		})
	}
}

type policyMismatchDependencies struct {
	*CurveDeriver
	plan             profilederivation.CurvePlan
	resolves, writes int
}

func (d *policyMismatchDependencies) Resolve(context.Context, *api.BatchEnergyOptimization) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error) {
	d.resolves++
	return d.plan, batchenergyoptimization.StoredObjectReference{}, nil, nil
}

func (d *policyMismatchDependencies) Write(context.Context, *api.BatchEnergyOptimization, batchenergyoptimization.StoredObjectReference, []api.ProfilingRun, batchenergyoptimization.Result) (batchenergyoptimization.StoredObjectReference, error) {
	d.writes++
	return batchenergyoptimization.StoredObjectReference{}, errors.New("unexpected result write")
}

type campaignStatusPatchClient struct {
	client.Client
	patches int
	err     error
}

func (c *campaignStatusPatchClient) Status() client.SubResourceWriter {
	return &campaignStatusPatchWriter{SubResourceWriter: c.Client.Status(), owner: c}
}

type campaignStatusPatchWriter struct {
	client.SubResourceWriter
	owner *campaignStatusPatchClient
}

func (w *campaignStatusPatchWriter) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	w.owner.patches++
	if w.owner.err != nil {
		return w.owner.err
	}
	return w.SubResourceWriter.Patch(ctx, object, patch, opts...)
}

func newPolicyMismatchCampaignFixture(t *testing.T, excess bool) (*api.BatchEnergyOptimization, []client.Object, *fakeCurveEvidenceLoader, *policyMismatchDependencies) {
	t.Helper()
	parent := testParent()
	parent.Generation = 1
	parent.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}
	base := curveFixtureRunConfig(t)
	base.State.Firstfloor.MaxBatchSize = 8
	idle := base
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
	}
	spec, err := profilerprojection.ProjectCurvePlanSpec(
		"policy-mismatch", base, idle, profilederivation.CurveConversionContract{}, []int{1, 2, 4, 8},
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &fakeCurveEvidenceLoader{
		active: make(map[string]profilerevidence.VerifiedEvidence),
		idle:   make(map[string]profilerevidence.VerifiedIdleEvidence),
	}
	deriver, err := NewCurveDeriver(evidence)
	if err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{parent}
	appendRun := func(mode batchenergyoptimization.WorkloadMode, batch, attempt int) *api.ProfilingRun {
		t.Helper()
		run := resultWriterRun(t, parent, batchenergyoptimization.Measurement{WorkloadMode: mode, Batch: batch}, attempt, types.UID(fmt.Sprintf("%s-%d-%d", mode, batch, attempt)))
		run.Status.Evidence = resultWriterEvidence(string(run.UID), 1)
		objects = append(objects, run)
		return run
	}
	for _, batch := range []int{1, 2, 4, 8} {
		count := 3
		if batch == 8 && excess {
			count = 4
		}
		config := base
		config.Measurement.Batch = batch
		for attempt := 1; attempt <= count; attempt++ {
			run := appendRun(batchenergyoptimization.WorkloadModeActive, batch, attempt)
			evidence.active[run.Name] = curveFixtureActiveEvidence(t, config, string(run.UID)+"-pod", 4+8/float64(batch))
		}
	}
	for attempt := 1; attempt <= 3; attempt++ {
		run := appendRun(batchenergyoptimization.WorkloadModeIdle, 1, attempt)
		evidence.idle[run.Name] = curveFixtureIdleEvidence(t, idle, string(run.UID)+"-pod", 4)
	}
	return parent, objects, evidence, &policyMismatchDependencies{CurveDeriver: deriver, plan: plan}
}

func assertCampaignPolicyMismatchStatus(t *testing.T, parent *api.BatchEnergyOptimization) {
	t.Helper()
	status := parent.Status
	if status.Phase != "Failed" || status.Outcome != terminalReasonEnergyObjectivePolicyMismatch ||
		status.CompletedTrials != 16 || status.ObservedGeneration != parent.Generation ||
		status.Result != nil || len(status.Conditions) != 1 {
		t.Fatalf("status=%+v", status)
	}
	condition := status.Conditions[0]
	if condition.Status != metav1.ConditionFalse || condition.Reason != terminalReasonEnergyObjectivePolicyMismatch ||
		condition.ObservedGeneration != parent.Generation ||
		!strings.Contains(condition.Message, "active/b8: expected batch=8, expected=3, completed=4") {
		t.Fatalf("condition=%+v", condition)
	}
}

func assertCampaignRunsRetained(t *testing.T, native client.Client, objects []client.Object) {
	t.Helper()
	ctx := context.Background()
	retained := &api.ProfilingRunList{}
	if err := native.List(ctx, retained); err != nil {
		t.Fatal(err)
	}
	if len(retained.Items) != len(objects)-1 {
		t.Fatalf("retained Runs=%d, want %d", len(retained.Items), len(objects)-1)
	}
	for _, object := range objects[1:] {
		before := object.(*api.ProfilingRun)
		after := &api.ProfilingRun{}
		if err := native.Get(ctx, client.ObjectKeyFromObject(before), after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before.Status, after.Status) {
			t.Fatalf("Run %s evidence/status changed: before=%+v after=%+v", before.Name, before.Status, after.Status)
		}
	}
}

func TestCampaignEnergyObjectivePolicyMismatchIsTerminal(t *testing.T) {
	const temporaryFailure = "temporary derivation error"
	const statusPatchFailure = "status patch error"
	for _, failure := range []string{"policy mismatch", temporaryFailure, statusPatchFailure} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			parent, objects, evidence, dependencies := newPolicyMismatchCampaignFixture(t, failure != temporaryFailure)
			temporaryErr := errors.New("temporary evidence load failure")
			patchErr := errors.New("status unavailable")
			if failure == temporaryFailure {
				evidence.err = temporaryErr
			}
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			native := &campaignStatusPatchClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).WithObjects(objects...).Build()}
			if failure == statusPatchFailure {
				native.err = patchErr
			}
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, dependencies, dependencies, dependencies)
			if err != nil {
				t.Fatal(err)
			}
			key := types.NamespacedName{Namespace: parent.Namespace, Name: parent.Name}
			request := ctrl.Request{NamespacedName: key}
			result, err := reconciler.Reconcile(ctx, request)
			if result != (ctrl.Result{}) || dependencies.writes != 0 {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, dependencies.writes)
			}
			updated := &api.BatchEnergyOptimization{}
			if getErr := native.Get(ctx, key, updated); getErr != nil {
				t.Fatal(getErr)
			}
			switch failure {
			case "policy mismatch":
				if err != nil || native.patches != 1 {
					t.Fatalf("patches=%d err=%v", native.patches, err)
				}
				assertCampaignPolicyMismatchStatus(t, updated)
				loads := evidence.calls
				result, err = reconciler.Reconcile(ctx, request)
				if err != nil || result != (ctrl.Result{}) || native.patches != 1 ||
					dependencies.resolves != 1 || evidence.calls != loads || dependencies.writes != 0 {
					t.Fatalf("replay result=%+v err=%v patches=%d resolves=%d loads=%d/%d writes=%d", result, err, native.patches, dependencies.resolves, evidence.calls, loads, dependencies.writes)
				}
			case temporaryFailure:
				if !errors.Is(err, temporaryErr) || native.patches != 0 || updated.Status.Phase == "Failed" {
					t.Fatalf("temporary error=%v status=%+v patches=%d", err, updated.Status, native.patches)
				}
				_, err = reconciler.Reconcile(ctx, request)
				if !errors.Is(err, temporaryErr) || dependencies.resolves != 2 {
					t.Fatalf("retry error=%v resolves=%d", err, dependencies.resolves)
				}
			case statusPatchFailure:
				if !errors.Is(err, patchErr) || native.patches != 1 {
					t.Fatalf("patch error=%v patches=%d", err, native.patches)
				}
			}
			assertCampaignRunsRetained(t, native, objects)
		})
	}
}
