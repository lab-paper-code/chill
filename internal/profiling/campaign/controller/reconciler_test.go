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
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCampaignNameLabelBoundariesProgressAndRestore(t *testing.T) {
	for _, name := range []string{
		"jetsono-ortcpu-m50",
		strings.Repeat("a", 63),
		strings.Repeat("a", 64),
		"mobilenet-v2-050-genesis3-gpu-1500-firstfloor-ort-cuda-amd64-20261105",
		strings.Repeat("a", 200), // The BEO admission CEL limit preserves room for trial suffixes.
	} {
		t.Run(fmt.Sprintf("name-%d", len(name)), func(t *testing.T) {
			ctx := context.Background()
			parent := testParent()
			parent.Name, parent.Generation = name, 1
			parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
			parent.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}
			reportBytes, report := curvePlanCandidateReport(t, parent)
			preparer := &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"}
			native := newCampaignMetadataValidationClient(t, parent, curvePlanModel(report))
			store := newFakeCurvePlanStore()
			options := CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20}
			resolver := &fakeCurvePlanCandidateResolver{reportBytes: reportBytes}
			source, err := NewCurvePlanSource(native, resolver, preparer, store, options)
			if err != nil {
				t.Fatal(err)
			}
			dependencies := &terminalReplayDependencies{}
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, source.Resolve, dependencies.Derive, dependencies.Write)
			if err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
			if _, err := reconciler.Reconcile(ctx, request); err != nil {
				t.Fatalf("initial campaign progress: %v", err)
			}
			idle := batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1}
			child := &api.ProfilingRun{}
			if err := native.Get(ctx, client.ObjectKey{Namespace: parent.Namespace, Name: batchenergyoptimization.RunName(name, idle, 1)}, child); err != nil {
				t.Fatal(err)
			}
			if !controlledByParent(parent, child) || child.OwnerReferences[0].Name != name ||
				(len(name) <= 63 && child.Labels[labelBatchEnergyOptimization] != name) {
				t.Fatalf("child identity changed: %+v", child.ObjectMeta)
			}
			prepared := len(preparer.requests)
			preparer.err = errors.New("restart must recover the frozen plan without preparing it again")
			recovered, err := NewCurvePlanSource(native, resolver, preparer, store, options)
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewBatchEnergyOptimizationReconciler(native, recovered.Resolve, dependencies.Derive, dependencies.Write)
			if err != nil {
				t.Fatal(err)
			}
			result, err := restarted.Reconcile(ctx, request)
			if err != nil || len(preparer.requests) != prepared || store.puts != 1 {
				t.Fatalf("restore pending child: result=%+v err=%v prepares=%d puts=%d", result, err, len(preparer.requests), store.puts)
			}
			child.Status.Evidence = &api.ProfilingRunEvidenceReference{Store: "evidence", Key: "verified"}
			if err := native.Status().Update(ctx, child); err != nil {
				t.Fatal(err)
			}
			if _, err := restarted.Reconcile(ctx, request); err != nil {
				t.Fatalf("progress from recovered evidence: %v", err)
			}
			updated := &api.BatchEnergyOptimization{}
			if err := native.Get(ctx, request.NamespacedName, updated); err != nil {
				t.Fatal(err)
			}
			runs, err := restarted.listRuns(ctx, parent)
			if err != nil || len(runs) != 2 || updated.Status.Phase != batchEnergyOptimizationPhaseMeasuring || updated.Status.CompletedTrials != 1 {
				t.Fatalf("recovered progress: runs=%d status=%+v err=%v", len(runs), updated.Status, err)
			}
			// A matching candidate label never authorizes a recreated same-name
			// parent's child, including a deliberate candidate-token collision.
			foreign, err := BuildRun(parent, idle, 99, "ort-cpu")
			if err != nil {
				t.Fatal(err)
			}
			foreign.OwnerReferences[0].UID = "stale-parent-uid"
			if err := native.Create(ctx, foreign); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := recovered.Resolve(ctx, parent); err == nil {
				t.Fatalf("curve-plan recovery adopted foreign UID: %v", err)
			}
			runs, err = restarted.listRuns(ctx, parent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ProjectSnapshot(parent, runs, nil, []int{1}, false); err == nil {
				t.Fatalf("snapshot adopted foreign UID: %v", err)
			}
		})
	}
}

func newCampaignMetadataValidationClient(
	t *testing.T,
	parent *api.BatchEnergyOptimization,
	model *api.ModelSpec,
) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// The active child uses the same bounded metadata contract.
	active, err := BuildRun(parent, batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1}, 1, "ort-cpu")
	if err != nil {
		t.Fatal(err)
	}
	if errs := apivalidation.ValidateObjectMeta(&active.ObjectMeta, true, apivalidation.NameIsDNSSubdomain, field.NewPath("metadata")); len(errs) != 0 {
		t.Fatal(errs.ToAggregate())
	}
	// Fake clients accept invalid metadata and selector values. Apply the
	// Kubernetes boundary validators before delegating to fake storage.
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(parent, &api.ProfilingRun{}).
		WithObjects(parent, model).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
				if errs := apivalidation.ValidateObjectMetaAccessor(object, true, apivalidation.NameIsDNSSubdomain, field.NewPath("metadata")); len(errs) != 0 {
					return errs.ToAggregate()
				}
				return c.Create(ctx, object, opts...)
			},
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				options := (&client.ListOptions{}).ApplyOptions(opts)
				if options.LabelSelector != nil {
					if _, err := metav1.ParseToLabelSelector(options.LabelSelector.String()); err != nil {
						return err
					}
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()
}

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
	_, _, err := completedRuns(batchenergyoptimization.Snapshot{
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
func (d *terminalReplayDependencies) Write(context.Context, *api.BatchEnergyOptimization, batchenergyoptimization.StoredObjectReference, []api.ProfilingRun, batchenergyoptimization.Result) (batchenergyoptimization.StoredObjectReference, batchenergyoptimization.Result, error) {
	d.writes++
	return batchenergyoptimization.StoredObjectReference{}, batchenergyoptimization.Result{}, errors.New("unexpected evidence recreation")
}

func TestTerminalCampaignReplayDoesNotRecreateEvidenceButResolverFailureStillRetries(t *testing.T) {
	for _, test := range []struct {
		name, phase, reason string
		status              metav1.ConditionStatus
		terminal            bool
	}{
		{"complete", "Complete", "ResultWritten", metav1.ConditionTrue, true},
		{"exhausted attempts", batchEnergyOptimizationPhaseFailed, terminalReasonRetryLimitExhausted, metav1.ConditionFalse, true},
		{"failed transition", batchEnergyOptimizationPhaseFailed, terminalReasonTransitionFailed, metav1.ConditionFalse, true},
		{"energy objective policy mismatch", batchEnergyOptimizationPhaseFailed, terminalReasonEnergyObjectivePolicyMismatch, metav1.ConditionFalse, true},
		{"recoverable resolver failure", batchEnergyOptimizationPhaseFailed, "CurvePlanResolutionFailed", metav1.ConditionFalse, false},
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
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, dependencies.Resolve, dependencies.Derive, dependencies.Write)
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

func TestCampaignInvalidChildContractStopsWithVisibleFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*api.ProfilingRun)
		message string
	}{
		{"missing identity", func(run *api.ProfilingRun) { run.Spec.CampaignTrial = nil }, "requires a positive logical batch"},
		{"contradictory identity", func(run *api.ProfilingRun) { run.Spec.CampaignTrial.Batch = 99 }, "disagrees with its logical trial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, objects, _, dependencies := newPolicyMismatchCampaignFixture(t, false)
			test.mutate(objects[1].(*api.ProfilingRun))
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			native := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).WithObjects(objects...).Build()
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, dependencies.Resolve, dependencies.Derive, dependencies.Write)
			if err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
			result, err := reconciler.Reconcile(context.Background(), request)
			if err != nil || result != (ctrl.Result{}) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			updated := &api.BatchEnergyOptimization{}
			if err := native.Get(context.Background(), request.NamespacedName, updated); err != nil {
				t.Fatal(err)
			}
			if updated.Status.Phase != batchEnergyOptimizationPhaseFailed ||
				updated.Status.Outcome != terminalReasonInvalidCampaignRun ||
				updated.Status.ObservedGeneration != parent.Generation || len(updated.Status.Conditions) != 1 {
				t.Fatalf("status=%+v", updated.Status)
			}
			condition := updated.Status.Conditions[0]
			if condition.Status != metav1.ConditionFalse || condition.Reason != terminalReasonInvalidCampaignRun ||
				!strings.Contains(condition.Message, test.message) {
				t.Fatalf("condition=%+v", condition)
			}
			result, err = reconciler.Reconcile(context.Background(), request)
			if err != nil || result != (ctrl.Result{}) || dependencies.resolves != 1 || dependencies.writes != 0 {
				t.Fatalf("terminal replay: result=%+v err=%v resolves=%d writes=%d", result, err, dependencies.resolves, dependencies.writes)
			}
		})
	}
}

func TestCampaignCurvePlanFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name     string
		failure  error
		mutate   func(*api.BatchEnergyOptimization)
		terminal bool
	}{
		{name: "invalid protocol", mutate: func(parent *api.BatchEnergyOptimization) { parent.Spec.ActiveProtocol.DurationSeconds = 0 }, terminal: true},
		{name: "temporary resolver failure", failure: errors.New("resolver temporarily unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			parent := testParent()
			parent.Generation = 1
			parent.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}
			if test.mutate != nil {
				test.mutate(parent)
			}
			reportBytes, report := curvePlanCandidateReport(t, parent)
			preparer := &fakeCurvePlanPreparer{
				report: report, reportBytes: reportBytes, nodeUID: "node-uid",
				err: test.failure,
			}
			store := newFakeCurvePlanStore()
			source, err := NewCurvePlanSource(
				&fakeCurvePlanModelReader{model: curvePlanModel(report)},
				&fakeCurvePlanCandidateResolver{reportBytes: reportBytes}, preparer, store,
				CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
			)
			if err != nil {
				t.Fatal(err)
			}
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			native := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).WithObjects(parent).Build()
			dependencies := &terminalReplayDependencies{}
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, source.Resolve, dependencies.Derive, dependencies.Write)
			if err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
			for attempt := range 2 {
				result, reconcileErr := reconciler.Reconcile(ctx, request)
				if (reconcileErr == nil) != test.terminal || result != (ctrl.Result{}) {
					t.Fatalf("attempt %d: result=%+v error=%v, terminal=%v", attempt, result, reconcileErr, test.terminal)
				}
			}
			updated := &api.BatchEnergyOptimization{}
			if err := native.Get(ctx, request.NamespacedName, updated); err != nil {
				t.Fatal(err)
			}
			reason, opens := "CurvePlanResolutionFailed", 2
			if test.terminal {
				reason, opens = profiler.ResolutionReasonInvalidRunSpec, 1
			}
			if updated.Status.Phase != batchEnergyOptimizationPhaseFailed || updated.Status.Outcome != reason ||
				updated.Status.ObservedGeneration != parent.Generation || len(updated.Status.Conditions) != 1 {
				t.Fatalf("failure status = %+v", updated.Status)
			}
			condition := updated.Status.Conditions[0]
			if condition.Type != "MeasurementComplete" || condition.Status != metav1.ConditionFalse ||
				condition.Reason != reason || condition.ObservedGeneration != parent.Generation || condition.Message == "" {
				t.Fatalf("failure condition = %+v", condition)
			}
			runs := &api.ProfilingRunList{}
			if err := native.List(ctx, runs); err != nil {
				t.Fatal(err)
			}
			if store.opens != opens || store.puts != 0 || len(runs.Items) != 0 || dependencies.derives != 0 || dependencies.writes != 0 {
				t.Fatalf("opens=%d puts=%d children=%d derives=%d writes=%d", store.opens, store.puts, len(runs.Items), dependencies.derives, dependencies.writes)
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
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, dependencies.Resolve, dependencies.Derive, dependencies.Write)
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

func (d *policyMismatchDependencies) Write(context.Context, *api.BatchEnergyOptimization, batchenergyoptimization.StoredObjectReference, []api.ProfilingRun, batchenergyoptimization.Result) (batchenergyoptimization.StoredObjectReference, batchenergyoptimization.Result, error) {
	d.writes++
	return batchenergyoptimization.StoredObjectReference{}, batchenergyoptimization.Result{}, errors.New("unexpected result write")
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

func TestCampaignRecoversImmutableWinnerAfterStatusPatchFailure(t *testing.T) {
	ctx := context.Background()
	parent, objects, _, dependencies := newPolicyMismatchCampaignFixture(t, false)
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	native := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(parent, &api.ProfilingRun{}).WithObjects(objects...).Build()
	patchFailure := errors.New("status unavailable")
	kube := &campaignStatusPatchClient{Client: native, err: patchFailure}
	store := newFakeCurvePlanStore()
	writer := newResultWriter(t, store, 1<<20)
	detail := "first measured result"
	derive := func(context.Context, profilederivation.CurvePlan, []api.ProfilingRun, []api.ProfilingRun, []int, profilederivation.EnergyObjectivePolicy) (batchenergyoptimization.CurveResult, error) {
		return batchenergyoptimization.CurveResult{Detail: detail}, nil
	}
	resolve := func(context.Context, *api.BatchEnergyOptimization) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error) {
		return dependencies.plan, resultWriterCurvePlanReference(), nil, nil
	}
	reconciler, err := NewBatchEnergyOptimizationReconciler(kube, resolve, derive, writer.Write)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
	if _, err := reconciler.Reconcile(ctx, request); !errors.Is(err, patchFailure) {
		t.Fatalf("status failure lost: %v", err)
	}
	detail = "recomputed after restart"
	kube.err = nil
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	current := &api.BatchEnergyOptimization{}
	if err := native.Get(ctx, request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	record, err := batchenergyoptimization.DecodeResultRecord(store.objects[store.lastIdentity.Location()])
	if err != nil {
		t.Fatal(err)
	}
	retained, ok, err := batchenergyoptimization.DecodeDiscoveryResult(record, dependencies.plan.PlannedBatchSizes())
	if err != nil || !ok || retained.Energy != "first measured result" ||
		current.Status.Outcome != string(record.Spec().Outcome) || current.Status.Result == nil {
		t.Fatalf("immutable winner/status diverged: detail=%+v status=%+v error=%v", retained, current.Status, err)
	}
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
	spec, err := profilerprojection.ProjectCurvePlanSpec("policy-mismatch", base, idle, profilederivation.CurveConversionContract{}, []int{1, 2, 4, 8})
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
		evidence.idle[run.Name] = curveFixtureIdleEvidence(t, idle, string(run.UID)+"-pod")
	}
	return parent, objects, evidence, &policyMismatchDependencies{CurveDeriver: deriver, plan: plan}
}

func assertCampaignPolicyMismatchStatus(t *testing.T, parent *api.BatchEnergyOptimization) {
	t.Helper()
	status := parent.Status
	if status.Phase != batchEnergyOptimizationPhaseFailed || status.Outcome != terminalReasonEnergyObjectivePolicyMismatch ||
		status.CompletedTrials != 16 || status.ObservedGeneration != parent.Generation ||
		status.Result != nil || len(status.Conditions) != 1 {
		t.Fatalf("status=%+v", status)
	}
	condition := status.Conditions[0]
	if condition.Status != metav1.ConditionFalse || condition.Reason != terminalReasonEnergyObjectivePolicyMismatch ||
		condition.ObservedGeneration != parent.Generation ||
		!strings.Contains(condition.Message, "active/b8: expected=3, completed=4") {
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
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, dependencies.Resolve, dependencies.Derive, dependencies.Write)
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
				if !errors.Is(err, temporaryErr) || native.patches != 0 || updated.Status.Phase == batchEnergyOptimizationPhaseFailed {
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

type campaignBoundaryScenario struct {
	name         string
	failedBatch  int
	stage        string
	failedIdle   bool
	runtimeError bool
	unclassified bool
	wantFailure  string
	wantPrefix   []int
}

func TestCampaignPublishesOnlyCompletePrefixAtActualExecutionBoundary(t *testing.T) {
	for _, test := range []campaignBoundaryScenario{
		{name: "partial batch four", failedBatch: 4, stage: "Execution", wantPrefix: []int{1, 2}},
		{name: "empty batch one", failedBatch: 1, stage: "Execution", wantPrefix: []int{}},
		{name: "idle runtime boundary", failedBatch: 1, failedIdle: true, stage: "Execution", wantPrefix: []int{}},
		{name: "genuine evidence failure", failedBatch: 1, stage: "ResultValidation", wantFailure: string(batchenergyoptimization.FailureClassFatalFailure)},
		{name: "generic runtime crash is fatal", failedBatch: 1, stage: "Execution", runtimeError: true, wantFailure: string(batchenergyoptimization.FailureClassFatalFailure)},
		{name: "startup OOM is unknown", failedBatch: 1, stage: "Startup", wantFailure: string(batchenergyoptimization.FailureClassUnknownFailure)},
		{name: "unclassified OOM is unknown", failedBatch: 1, stage: "Execution", unclassified: true, wantFailure: string(batchenergyoptimization.FailureClassUnknownFailure)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			parent, templates, evidence, dependencies := newPolicyMismatchCampaignFixture(t, false)
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			native := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(parent, &api.ProfilingRun{}).WithObjects(parent).Build()
			store := newFakeCurvePlanStore()
			writer := newResultWriter(t, store, 1<<20)
			curveCalls := 0
			derive := func(ctx context.Context, plan profilederivation.CurvePlan, active, idle []api.ProfilingRun, batches []int, policy profilederivation.EnergyObjectivePolicy) (batchenergyoptimization.CurveResult, error) {
				curveCalls++
				for _, run := range active {
					if int(run.Spec.CampaignTrial.Batch) >= test.failedBatch {
						t.Fatalf("partial failed batch entered curve: %s", run.Name)
					}
				}
				if len(active) != len(batches)*3 || len(idle) != 3 {
					t.Fatalf("curve evidence active=%d idle=%d batches=%v", len(active), len(idle), batches)
				}
				return dependencies.CurveDeriver.Derive(ctx, plan, active, idle, batches, policy)
			}
			resolve := func(context.Context, *api.BatchEnergyOptimization) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error) {
				return dependencies.plan, resultWriterCurvePlanReference(), nil, nil
			}
			reconciler, err := NewBatchEnergyOptimizationReconciler(native, resolve, derive, writer.Write)
			if err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
			created := 0
			for range 20 {
				if _, err := reconciler.Reconcile(ctx, request); err != nil {
					t.Fatal(err)
				}
				current := &api.BatchEnergyOptimization{}
				if err := native.Get(ctx, request.NamespacedName, current); err != nil {
					t.Fatal(err)
				}
				if current.Status.Phase == "Complete" || current.Status.Phase == batchEnergyOptimizationPhaseFailed {
					assertCampaignBoundaryResult(t, test, current, store, parent, dependencies.plan.PlannedBatchSizes())
					if test.failedBatch == 1 && (curveCalls != 0 || evidence.calls != 0) {
						t.Fatal("empty prefix loaded a curve")
					}
					if _, err := reconciler.Reconcile(ctx, request); err != nil {
						t.Fatal(err)
					}
					retained, err := reconciler.listRuns(ctx, parent)
					if err != nil || len(retained) != created {
						t.Fatalf("terminal replay created a retry: %d %v", len(retained), err)
					}
					return
				}
				created += executeCampaignBoundaryRun(t, ctx, reconciler, parent, test, templates, native)
			}
			t.Fatal("campaign did not stop")
		})
	}
}

func assertCampaignBoundaryResult(
	t *testing.T, test campaignBoundaryScenario, current *api.BatchEnergyOptimization,
	store *fakeCurvePlanStore, parent *api.BatchEnergyOptimization, plannedBatches []int,
) {
	t.Helper()
	if test.wantPrefix == nil {
		if current.Status.Phase != batchEnergyOptimizationPhaseFailed || current.Status.Result != nil || store.puts != 0 {
			t.Fatalf("genuine failure published discovery: %+v", current.Status)
		}
		message := current.Status.Conditions[0].Message
		failedRun := batchenergyoptimization.RunName(parent.Name, batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1}, 1)
		if current.Status.Outcome != test.wantFailure ||
			!strings.Contains(message, "class="+test.wantFailure) ||
			!strings.Contains(message, "failedK=1") || !strings.Contains(message, "run="+parent.Namespace+"/"+failedRun) {
			t.Fatalf("failed parent lost actual failure identity: %+v", current.Status)
		}
	} else {
		if current.Status.Phase != "Complete" || current.Status.Result == nil {
			t.Fatalf("expected boundary not complete: %+v", current.Status)
		}
		record, err := batchenergyoptimization.DecodeResultRecord(store.objects[store.lastIdentity.Location()])
		if err != nil {
			t.Fatal(err)
		}
		detail, ok, err := batchenergyoptimization.DecodeDiscoveryResult(record, plannedBatches)
		if err != nil || !ok || !reflect.DeepEqual(detail.MeasuredBatchSizes, test.wantPrefix) {
			t.Fatalf("published prefix=%+v ok=%v error=%v", detail, ok, err)
		}
		if detail.Stop.Run == nil || detail.Stop.Run.UID == "" || detail.Stop.Diagnostic == "" {
			t.Fatalf("actual termination reference missing: %+v", detail.Stop)
		}
		wantCompleted := 3 + len(test.wantPrefix)*3
		if test.failedIdle {
			wantCompleted = 0
		}
		if test.failedBatch == 4 {
			wantCompleted += 2
		}
		if len(record.Spec().CompletedTrials) != wantCompleted || len(record.Spec().ExcludedTrials) != 1 {
			t.Fatalf("retained evidence lost: %+v", record.Spec())
		}
	}
}

func executeCampaignBoundaryRun(
	t *testing.T, ctx context.Context, reconciler *BatchEnergyOptimizationReconciler,
	parent *api.BatchEnergyOptimization, test campaignBoundaryScenario,
	templates []client.Object, native client.Client,
) int {
	t.Helper()
	runs, err := reconciler.listRuns(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	for index := range runs {
		run := &runs[index]
		if run.Status.Evidence != nil || run.Status.HasTerminalFailure() {
			continue
		}
		pending++
		batch, attempt := int(run.Spec.CampaignTrial.Batch), int(run.Spec.CampaignTrial.Attempt)
		if batch > test.failedBatch || attempt > 3 {
			t.Fatalf("executed beyond boundary: %+v", run.Spec.CampaignTrial)
		}
		for _, object := range templates[1:] {
			template := object.(*api.ProfilingRun)
			if template.Name == run.Name {
				run.UID = template.UID
				run.Status = template.Status
			}
		}
		failedMode := activeWorkloadMode
		if test.failedIdle {
			failedMode = idleWorkloadMode
		}
		if run.Spec.Measurement.WorkloadMode == failedMode && batch == test.failedBatch &&
			(test.failedBatch == 1 || attempt == 3) {
			run.Status.Evidence = nil
			run.Status.Conditions = []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed"}}
			run.Status.RuntimeTermination = &api.ProfilingRunRuntimeTermination{
				Container: "runtime", Stage: test.stage, Reason: "OOMKilled", ExitCode: 137,
				Class:     "ExpectedStop",
				JobReason: "BackoffLimitExceeded", JobMessage: "runtime execution failed",
			}
			if test.runtimeError {
				run.Status.RuntimeTermination.Reason = "Error"
				run.Status.RuntimeTermination.FailureReason = "ExecutionFailed"
				run.Status.RuntimeTermination.ExitCode = 3
				run.Status.RuntimeTermination.Class = string(batchenergyoptimization.FailureClassFatalFailure)
			}
			if test.stage == "ResultValidation" {
				run.Status.Conditions = append(run.Status.Conditions, metav1.Condition{
					Type: api.ProfilingRunConditionEvidenceVerified, Status: metav1.ConditionFalse, Reason: "EvidenceInvalid",
				})
				run.Status.RuntimeTermination.Class = string(batchenergyoptimization.FailureClassFatalFailure)
			}
			if test.unclassified {
				run.Status.RuntimeTermination.Class = ""
			}
		}
		status := run.Status
		if err := native.Update(ctx, run); err != nil {
			t.Fatal(err)
		}
		run.Status = status
		if err := native.Status().Update(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if pending != 1 {
		t.Fatalf("pending children=%d", pending)
	}
	return pending
}

func TestRetryExhaustionStatusIdentifiesCurrentBatchAfterEarlierRecovery(t *testing.T) {
	ctx := context.Background()
	parent, _, _, dependencies := newPolicyMismatchCampaignFixture(t, false)
	parent.Spec.RetryLimit = 1
	objects := []client.Object{parent}
	appendRun := func(mode batchenergyoptimization.WorkloadMode, batch, attempt int, uid types.UID, cause string) *api.ProfilingRun {
		t.Helper()
		run := resultWriterRun(t, parent, batchenergyoptimization.Measurement{WorkloadMode: mode, Batch: batch}, attempt, uid)
		if cause == "" {
			run.Status.Evidence = resultWriterEvidence(string(uid), 1)
		} else {
			run.Status.Conditions = []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed"}}
			run.Status.RuntimeTermination = &api.ProfilingRunRuntimeTermination{
				Class: "RetryableFailure", Container: "runtime", Stage: "Execution", Reason: "Error", ExitCode: 1,
				FailureReason: "Evicted", PodReason: "Evicted", PodMessage: cause,
			}
		}
		objects = append(objects, run)
		return run
	}
	for attempt := 1; attempt <= 3; attempt++ {
		appendRun(batchenergyoptimization.WorkloadModeIdle, 1, attempt, types.UID(fmt.Sprintf("idle-%d", attempt)), "")
	}
	appendRun(batchenergyoptimization.WorkloadModeActive, 1, 1, "k1-success-1", "")
	appendRun(batchenergyoptimization.WorkloadModeActive, 1, 2, "k1-success-2", "")
	recoveredFailure := appendRun(batchenergyoptimization.WorkloadModeActive, 1, 3, "recovered-k1-failure", "earlier-recovered-node-cause")
	appendRun(batchenergyoptimization.WorkloadModeActive, 1, 4, "k1-success-4", "")
	appendRun(batchenergyoptimization.WorkloadModeActive, 2, 1, "k2-failure-1", "first-current-node-cause")
	exhaustingFailure := appendRun(batchenergyoptimization.WorkloadModeActive, 2, 2, "exhausting-k2-failure", "latest-current-node-cause")
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	native := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(parent).WithObjects(objects...).Build()
	resolve := func(context.Context, *api.BatchEnergyOptimization) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error) {
		return dependencies.plan, resultWriterCurvePlanReference(), nil, nil
	}
	unused := &terminalReplayDependencies{}
	reconciler, err := NewBatchEnergyOptimizationReconciler(native, resolve, unused.Derive, unused.Write)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	current := &api.BatchEnergyOptimization{}
	if err := native.Get(ctx, request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != batchEnergyOptimizationPhaseFailed || current.Status.Outcome != terminalReasonRetryLimitExhausted ||
		current.Status.Result != nil || unused.derives != 0 || unused.writes != 0 {
		t.Fatalf("retry exhaustion published or derived a result: status=%+v calls=%+v", current.Status, unused)
	}
	message := current.Status.Conditions[0].Message
	for _, fact := range []string{"failedK=2", "attempt=2", "run=" + exhaustingFailure.Namespace + "/" + exhaustingFailure.Name,
		"uid=" + string(exhaustingFailure.UID), exhaustingFailure.Status.RuntimeTermination.PodMessage} {
		if !strings.Contains(message, fact) {
			t.Fatalf("exhaustion status lost current failure %q: %s", fact, message)
		}
	}
	if strings.Contains(message, string(recoveredFailure.UID)) ||
		strings.Contains(message, recoveredFailure.Status.RuntimeTermination.PodMessage) {
		t.Fatalf("resolved earlier batch replaced the exhausted measurement: %s", message)
	}
	assertCampaignRunsRetained(t, native, objects)
}
