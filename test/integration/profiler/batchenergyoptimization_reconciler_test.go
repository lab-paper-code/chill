package profilerintegration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	batchcontroller "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeCurvePlanSource struct {
	plan          profilederivation.CurvePlan
	reference     batchenergyoptimization.StoredObjectReference
	transition    *batchcontroller.TransitionMaterial
	calls         int
	unexpectedErr error
}

func (s *fakeCurvePlanSource) Resolve(
	_ context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
) (
	profilederivation.CurvePlan,
	batchenergyoptimization.StoredObjectReference,
	*batchcontroller.TransitionMaterial,
	error,
) {
	s.calls++
	if s.unexpectedErr != nil {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, s.unexpectedErr
	}
	if parent == nil || parent.UID == "" {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil,
			fmt.Errorf("parent identity is missing")
	}
	return s.plan, s.reference, s.transition, nil
}

type fakeCurveDeriver struct {
	result        batchenergyoptimization.CurveResult
	continueUntil int
	calls         int
	activeRuns    []edgev1alpha1.ProfilingRun
	idleRuns      []edgev1alpha1.ProfilingRun
	batches       []int
	policy        profilederivation.EnergyObjectivePolicy
}

func (d *fakeCurveDeriver) Derive(
	_ context.Context,
	_ profilederivation.CurvePlan,
	activeRuns []edgev1alpha1.ProfilingRun,
	idleRuns []edgev1alpha1.ProfilingRun,
	batches []int,
	policy profilederivation.EnergyObjectivePolicy,
) (batchenergyoptimization.CurveResult, error) {
	d.calls++
	d.activeRuns = append([]edgev1alpha1.ProfilingRun(nil), activeRuns...)
	d.idleRuns = append([]edgev1alpha1.ProfilingRun(nil), idleRuns...)
	d.batches = append([]int(nil), batches...)
	d.policy = policy
	result := d.result
	if len(batches) < d.continueUntil {
		result.Continue = true
	}
	return result, nil
}

type fakeResultWriter struct {
	reference     batchenergyoptimization.StoredObjectReference
	calls         int
	curvePlan     batchenergyoptimization.StoredObjectReference
	runs          []edgev1alpha1.ProfilingRun
	result        batchenergyoptimization.Result
	unexpectedErr error
}

func (w *fakeResultWriter) Write(
	_ context.Context,
	_ *edgev1alpha1.BatchEnergyOptimization,
	curvePlan batchenergyoptimization.StoredObjectReference,
	runs []edgev1alpha1.ProfilingRun,
	result batchenergyoptimization.Result,
) (batchenergyoptimization.StoredObjectReference, error) {
	w.calls++
	w.curvePlan = curvePlan
	w.runs = append([]edgev1alpha1.ProfilingRun(nil), runs...)
	w.result = result
	return w.reference, w.unexpectedErr
}

func TestBatchEnergyOptimizationPlanFailureIsVisibleAndRetryable(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	parent := createBatchEnergyOptimization(t, ctx, kube, "batch-plan-failure", 2)
	plans, curves, results := completedBatchFakes(t)
	plans.unexpectedErr = fmt.Errorf("device power mode is unavailable")
	reconciler := newBatchReconciler(t, kube, plans, curves, results)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
	if _, err := reconciler.Reconcile(ctx, request); err == nil {
		t.Fatal("plan failure did not remain retryable by the controller")
	}
	failed := getBatchEnergyOptimization(t, ctx, kube, request.NamespacedName)
	if failed.Status.Phase != "Failed" || failed.Status.ObservedGeneration != parent.Generation ||
		len(failed.Status.Conditions) != 1 || failed.Status.Conditions[0].Status != metav1.ConditionFalse ||
		failed.Status.Conditions[0].Message != plans.unexpectedErr.Error() {
		t.Fatalf("plan failure was not exposed through API status: %#v", failed.Status)
	}
	runs := &edgev1alpha1.ProfilingRunList{}
	if err := kube.List(ctx, runs, client.InNamespace(parent.Namespace),
		client.MatchingLabels{"edge.dacs.io/batch-energy-optimization": parent.Name}); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 0 {
		t.Fatal("plan failure created measurement runs")
	}
	plans.unexpectedErr = nil
	reconcileBatch(t, ctx, reconciler, request)
	recovered := getBatchEnergyOptimization(t, ctx, kube, request.NamespacedName)
	if recovered.Status.Phase != "Measuring" || recovered.Status.Conditions[0].Status != metav1.ConditionUnknown {
		t.Fatalf("resolved plan did not resume measurement: %#v", recovered.Status)
	}
}

func TestBatchEnergyOptimizationReconcilerCompletesAcrossCollisionWaitRetryAndRestart(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	parent := createBatchEnergyOptimization(t, ctx, kube, "batch-control-completed", 2)
	plans, curves, results := completedBatchFakes(t)
	reconciler := newBatchReconciler(t, kube, plans, curves, results)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}

	stale := buildCampaignRun(t, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1,
	}, 9)
	stale.Name = parent.Name + "-stale-owner"
	stale.OwnerReferences[0].UID = types.UID("replaced-parent-uid")
	if err := kube.Create(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err == nil ||
		!strings.Contains(err.Error(), "not controller-owned by this BatchEnergyOptimization") {
		t.Fatalf("stale owner reconcile error = %v, want exact owner-UID rejection", err)
	}
	deleteRunAndWait(t, ctx, kube, stale)

	idle := batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1}
	blocker := buildCampaignRun(t, parent, idle, 1)
	blocker.Labels = nil
	blocker.OwnerReferences = nil
	if err := kube.Create(ctx, blocker); err != nil {
		t.Fatal(err)
	}
	result := reconcileBatch(t, ctx, reconciler, request)
	if !result.Requeue {
		t.Fatalf("AlreadyExists reconcile result = %+v, want an explicit requeue", result)
	}
	deleteRunAndWait(t, ctx, kube, blocker)

	reconcileBatch(t, ctx, reconciler, request)
	requireCampaignRun(t, ctx, kube, parent, idle, 1)
	requireOwnedRunCount(t, ctx, kube, parent.UID, 1)

	corruptBatchStatus(t, ctx, kube, parent, 41)
	restarted := newBatchReconciler(t, kube, plans, curves, results)
	waitResult := reconcileBatch(t, ctx, restarted, request)
	if waitResult.RequeueAfter == 0 {
		t.Fatalf("live-Run reconcile result = %+v, want wait backstop", waitResult)
	}
	requireOwnedRunCount(t, ctx, kube, parent.UID, 1)
	currentParent := getBatchEnergyOptimization(t, ctx, kube, client.ObjectKeyFromObject(parent))
	if currentParent.Status.CompletedTrials != 0 || currentParent.Status.Phase != "Measuring" {
		t.Fatalf("recomputed live status = %#v, want Measuring with zero completed trials", currentParent.Status)
	}
	measuringSince := currentParent.Status.Conditions[0].LastTransitionTime

	setCampaignRunOutcome(t, ctx, kube, parent, idle, 1, "Failed")
	reconcileBatch(t, ctx, restarted, request)
	requireCampaignRun(t, ctx, kube, parent, idle, 2)

	setCampaignRunOutcome(t, ctx, kube, parent, idle, 2, "Completed")
	reconcileBatch(t, ctx, restarted, request)
	requireCampaignRun(t, ctx, kube, parent, idle, 3)
	setCampaignRunOutcome(t, ctx, kube, parent, idle, 3, "Completed")
	reconcileBatch(t, ctx, restarted, request)
	requireCampaignRun(t, ctx, kube, parent, idle, 4)
	setCampaignRunOutcome(t, ctx, kube, parent, idle, 4, "Completed")

	activeBatchOne := batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1,
	}
	activeBatchTwo := batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}
	acceptActiveCampaignRuns(
		t, ctx, kube, parent, restarted, request, []batchenergyoptimization.Measurement{activeBatchOne, activeBatchTwo},
		measuringSince,
	)
	reconcileBatch(t, ctx, restarted, request)

	finished := getBatchEnergyOptimization(t, ctx, kube, client.ObjectKeyFromObject(parent))
	if finished.Status.Phase != "Complete" || finished.Status.Outcome != "MinimumFound/SearchSpaceExhausted" ||
		finished.Status.ObservedGeneration != finished.Generation || finished.Status.CompletedTrials != 9 ||
		finished.Status.Result == nil {
		t.Fatalf("terminal BatchEnergyOptimization status = %#v", finished.Status)
	}
	wantReference := results.reference
	gotReference := finished.Status.Result
	if gotReference.Store != wantReference.Store || gotReference.Key != wantReference.Key ||
		gotReference.SchemaVersion != wantReference.SchemaVersion || gotReference.Size != wantReference.Size {
		t.Fatalf("terminal result reference = %#v, want writer reference %+v", gotReference, wantReference)
	}
	wantActiveNames := []string{
		batchcontroller.RunName(parent.Name, activeBatchOne, 1),
		batchcontroller.RunName(parent.Name, activeBatchOne, 2),
		batchcontroller.RunName(parent.Name, activeBatchOne, 3),
		batchcontroller.RunName(parent.Name, activeBatchTwo, 1),
		batchcontroller.RunName(parent.Name, activeBatchTwo, 2),
		batchcontroller.RunName(parent.Name, activeBatchTwo, 3),
	}
	wantIdleNames := []string{
		batchcontroller.RunName(parent.Name, idle, 2),
		batchcontroller.RunName(parent.Name, idle, 3),
		batchcontroller.RunName(parent.Name, idle, 4),
	}
	if curves.calls != 2 || !runNamesEqual(curves.activeRuns, wantActiveNames) ||
		!runNamesEqual(curves.idleRuns, wantIdleNames) || len(curves.batches) != 2 ||
		curves.batches[0] != 1 || curves.batches[1] != 2 || curves.policy.TrialsPerBatch != 3 {
		t.Fatalf("curve derivation calls = %d, active=%d idle=%d batches=%v policy=%+v",
			curves.calls, len(curves.activeRuns), len(curves.idleRuns), curves.batches, curves.policy)
	}
	if results.calls != 1 || results.curvePlan != plans.reference || len(results.runs) != 10 ||
		results.result.Outcome != "MinimumFound/SearchSpaceExhausted" {
		t.Fatalf("result writer calls = %d, plan=%+v runs=%d result=%+v",
			results.calls, results.curvePlan, len(results.runs), results.result)
	}
}

func TestBatchEnergyOptimizationReconcilerExhaustsRetryLimit(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	parent := createBatchEnergyOptimization(t, ctx, kube, "batch-control-exhausted", 1)
	plans, curves, results := completedBatchFakes(t)
	reconciler := newBatchReconciler(t, kube, plans, curves, results)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
	idle := batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1}

	reconcileBatch(t, ctx, reconciler, request)
	requireCampaignRun(t, ctx, kube, parent, idle, 1)
	reconcileBatch(t, ctx, reconciler, request)
	requireOwnedRunCount(t, ctx, kube, parent.UID, 1)
	setCampaignRunOutcome(t, ctx, kube, parent, idle, 1, "Failed")
	reconcileBatch(t, ctx, reconciler, request)
	requireCampaignRun(t, ctx, kube, parent, idle, 2)
	reconcileBatch(t, ctx, reconciler, request)
	requireOwnedRunCount(t, ctx, kube, parent.UID, 2)
	setCampaignRunOutcome(t, ctx, kube, parent, idle, 2, "Failed")
	reconcileBatch(t, ctx, reconciler, request)

	failed := getBatchEnergyOptimization(t, ctx, kube, client.ObjectKeyFromObject(parent))
	if failed.Status.Phase != "Failed" || failed.Status.Outcome != "RetryLimitExhausted" ||
		failed.Status.CompletedTrials != 0 || failed.Status.Result != nil {
		t.Fatalf("exhausted BatchEnergyOptimization status = %#v", failed.Status)
	}
	if results.calls != 0 || curves.calls != 0 {
		t.Fatalf("terminal failure crossed derivation/write ports: derive=%d write=%d", curves.calls, results.calls)
	}
	resourceVersion := failed.ResourceVersion
	restarted := newBatchReconciler(t, kube, plans, curves, results)
	reconcileBatch(t, ctx, restarted, request)
	afterRestart := getBatchEnergyOptimization(t, ctx, kube, client.ObjectKeyFromObject(parent))
	if afterRestart.ResourceVersion != resourceVersion {
		t.Fatalf("stable terminal failure was rewritten: before=%s after=%s", resourceVersion, afterRestart.ResourceVersion)
	}
	requireOwnedRunCount(t, ctx, kube, parent.UID, 2)
}

func completedBatchFakes(
	t *testing.T,
) (*fakeCurvePlanSource, *fakeCurveDeriver, *fakeResultWriter) {
	t.Helper()
	plan := integrationCurvePlan(t)
	plans := &fakeCurvePlanSource{
		plan: plan,
		reference: batchenergyoptimization.StoredObjectReference{
			Store: "plan-store", Key: "plans/from-source.json",
			SchemaVersion: "source-plan-schema", Size: 701,
		},
	}
	curves := &fakeCurveDeriver{
		continueUntil: 2,
		result: batchenergyoptimization.CurveResult{
			Outcome: "MinimumFound/SearchSpaceExhausted",
			Detail:  map[string]any{"bestBatch": 2},
		},
	}
	results := &fakeResultWriter{reference: batchenergyoptimization.StoredObjectReference{
		Store: "writer-store", Key: "writer-returned/result-object.json",
		SchemaVersion: "writer-returned-schema", Size: 9876,
	}}
	return plans, curves, results
}

func newBatchReconciler(
	t *testing.T,
	kube client.Client,
	plans *fakeCurvePlanSource,
	curves *fakeCurveDeriver,
	results *fakeResultWriter,
) *batchcontroller.BatchEnergyOptimizationReconciler {
	t.Helper()
	reconciler, err := batchcontroller.NewBatchEnergyOptimizationReconciler(kube, plans, curves, results)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func createBatchEnergyOptimization(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	name string,
	retryLimit int32,
) *edgev1alpha1.BatchEnergyOptimization {
	t.Helper()
	parent := &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: profilerIntegrationNamespace},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec: "mobilenet-v2-050", DeviceClass: "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu", PowerMode: "fixed",
				},
				TargetNode: "lattepanda", CPURequest: resource.MustParse("100m"),
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{
				WarmupIterations: 20, DurationSeconds: 30,
				InputGenerator: "numpy-default-rng-seed-0-inputs-v1", OutputValidation: "nonempty-shape-batch-v1",
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000, TimeoutMilliseconds: 900, DurationSeconds: 31,
				},
			},
			IdleProtocol: edgev1alpha1.IdleProtocolSpec{
				SettlingDurationSeconds: 5, DurationSeconds: 30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000, TimeoutMilliseconds: 900, DurationSeconds: 31,
				},
			},
			TrialsPerBatch: 3,
			RetryLimit:     retryLimit,
		},
	}
	if err := kube.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(parent), parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func runNamesEqual(runs []edgev1alpha1.ProfilingRun, want []string) bool {
	if len(runs) != len(want) {
		return false
	}
	for index := range runs {
		if runs[index].Name != want[index] {
			return false
		}
	}
	return true
}

func acceptActiveCampaignRuns(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	parent *edgev1alpha1.BatchEnergyOptimization,
	reconciler *batchcontroller.BatchEnergyOptimizationReconciler,
	request ctrl.Request,
	measurements []batchenergyoptimization.Measurement,
	measuringSince metav1.Time,
) {
	t.Helper()
	for measurementIndex, measurement := range measurements {
		for attempt := 1; attempt <= 3; attempt++ {
			reconcileBatch(t, ctx, reconciler, request)
			requireCampaignRun(t, ctx, kube, parent, measurement, attempt)
			setCampaignRunOutcome(t, ctx, kube, parent, measurement, attempt, "Completed")
			if measurementIndex == 0 && attempt == 1 {
				measuring := getBatchEnergyOptimization(t, ctx, kube, client.ObjectKeyFromObject(parent))
				if !measuring.Status.Conditions[0].LastTransitionTime.Equal(&measuringSince) {
					t.Fatalf("measuring transition time moved from %s to %s",
						measuringSince, measuring.Status.Conditions[0].LastTransitionTime)
				}
			}
		}
	}
}

func buildCampaignRun(
	t *testing.T,
	parent *edgev1alpha1.BatchEnergyOptimization,
	measurement batchenergyoptimization.Measurement,
	attempt int,
) *edgev1alpha1.ProfilingRun {
	t.Helper()
	run, err := batchcontroller.BuildRun(parent, measurement, attempt)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func reconcileBatch(
	t *testing.T,
	ctx context.Context,
	reconciler *batchcontroller.BatchEnergyOptimizationReconciler,
	request ctrl.Request,
) ctrl.Result {
	t.Helper()
	result, err := reconciler.Reconcile(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireCampaignRun(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	parent *edgev1alpha1.BatchEnergyOptimization,
	measurement batchenergyoptimization.Measurement,
	attempt int,
) *edgev1alpha1.ProfilingRun {
	t.Helper()
	run := &edgev1alpha1.ProfilingRun{}
	key := client.ObjectKey{
		Namespace: parent.Namespace,
		Name:      batchcontroller.RunName(parent.Name, measurement, attempt),
	}
	if err := kube.Get(ctx, key, run); err != nil {
		t.Fatal(err)
	}
	owner := metav1.GetControllerOf(run)
	if owner == nil || owner.UID != parent.UID {
		t.Fatalf("ProfilingRun %s owner = %#v, want parent UID %s", key, owner, parent.UID)
	}
	return run
}

func setCampaignRunOutcome(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	parent *edgev1alpha1.BatchEnergyOptimization,
	measurement batchenergyoptimization.Measurement,
	attempt int,
	verdict string,
) {
	t.Helper()
	run := requireCampaignRun(t, ctx, kube, parent, measurement, attempt)

	if verdict == "Completed" {
		run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{
			Store: "run-evidence", Key: fmt.Sprintf("runs/%s/execution.json", run.UID),
			SchemaVersion: "run-evidence-schema", Size: 1000 + int64(attempt),
		}
	} else {
		run.Status.Conditions = []metav1.Condition{{
			Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed", LastTransitionTime: metav1.Now(),
		}}
	}
	if err := kube.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func requireOwnedRunCount(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	parentUID types.UID,
	want int,
) {
	t.Helper()
	runs := &edgev1alpha1.ProfilingRunList{}
	if err := kube.List(ctx, runs, client.InNamespace(profilerIntegrationNamespace)); err != nil {
		t.Fatal(err)
	}
	count := 0
	for index := range runs.Items {
		owner := metav1.GetControllerOf(&runs.Items[index])
		if owner != nil && owner.UID == parentUID {
			count++
		}
	}
	if count != want {
		t.Fatalf("owned ProfilingRun count = %d, want %d", count, want)
	}
}

func corruptBatchStatus(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	parent *edgev1alpha1.BatchEnergyOptimization,
	accepted int32,
) {
	t.Helper()
	current := getBatchEnergyOptimization(t, ctx, kube, client.ObjectKeyFromObject(parent))
	current.Status = edgev1alpha1.BatchEnergyOptimizationStatus{
		ObservedGeneration: current.Generation,
		Phase:              "Failed",
		CompletedTrials:    accepted,
		Outcome:            "StaleStatusMustNotDriveDecision",
	}
	if err := kube.Status().Update(ctx, current); err != nil {
		t.Fatal(err)
	}
}

func getBatchEnergyOptimization(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	key client.ObjectKey,
) *edgev1alpha1.BatchEnergyOptimization {
	t.Helper()
	parent := &edgev1alpha1.BatchEnergyOptimization{}
	if err := kube.Get(ctx, key, parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func deleteRunAndWait(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	run *edgev1alpha1.ProfilingRun,
) {
	t.Helper()
	key := client.ObjectKeyFromObject(run)
	if err := kube.Delete(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 5*time.Second, true,
		func(ctx context.Context) (bool, error) {
			err := kube.Get(ctx, key, &edgev1alpha1.ProfilingRun{})
			return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
		}); err != nil {
		t.Fatal(err)
	}
}

func integrationCurvePlan(t *testing.T) profilederivation.CurvePlan {
	t.Helper()
	image := func(name, character string) string {
		return "registry.example/chill/" + name + "@sha256:" + strings.Repeat(character, 64)
	}
	protocol := func(mode string) profilederivation.CurveProtocolContract {
		return profilederivation.CurveProtocolContract{
			WorkloadMode: mode, InputGenerator: "seeded", OutputValidation: "shape",
			DurationSeconds: 30, MetricIntervalMilliseconds: 1000,
			MetricTimeoutMilliseconds: 900, MetricDurationSeconds: 31,
			MetricObserverImage: image("metric", "a"), WorkloadObserverImage: image("workload", "b"),
			WorkloadSchemaVersion: "chill.dacs.io/workload-contract.v1alpha1",
		}
	}
	active := protocol("saturated-serial-fixed-batch")
	active.WarmupIterations = 20
	idle := protocol("idle-model-loaded")
	idle.InputGenerator = "none"
	idle.OutputValidation = "none"
	idle.SettlingDurationSeconds = 5
	plan, err := profilederivation.NewCurvePlan(profilederivation.CurvePlanSpec{
		Name: "integration-curve", LogicalModel: "mobilenet-v2-050",
		DeviceClass: "lattepanda-3-delta-8g", NodeUID: "node-uid",
		RuntimeFamily: "ort", Backend: "cpu", RuntimeImage: image("runtime", "c"), PowerMode: "fixed",
		CPU: profilederivation.CurveCPUContract{
			Policy: "allocatable", PolicyVersion: "v1", Request: "100m", Limit: "1",
		},
		MetricSource: profilederivation.CurveMetricSourceContract{
			SourceKind: "prometheus", NodeName: "lattepanda", Endpoint: "http://metrics",
			PowerMetric: "power_watts",
		},
		ActiveProtocol: active, IdleProtocol: idle,
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: "ort-cpu", Name: "mobilenet", Path: "/models/mobilenet.onnx",
			Image: image("artifact", "d"),
		},
		PlannedBatchSizes: []int{1, 2},
		Tensor: profilederivation.CurveTensorContract{
			Tensors: []modelcompat.InputTensor{{Name: "input", Datatype: "FP32", SampleShape: []int32{3, 224, 224}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
