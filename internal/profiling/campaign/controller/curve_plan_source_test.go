package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const curvePlanWorkloadUID = "workload-uid"

func TestCurvePlanSourcePlansAutomaticDomainWithBatchOneIdle(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(fmt.Sprintf("declared=%t", declared), func(t *testing.T) {
			parent := testParent()
			parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
			_, report := curvePlanCandidateReport(t, parent)
			model := curvePlanModel(report)
			if declared {
				report.ArtifactCapability.DeclaredBatchSizes = []int32{1, 4}
				model.Spec.Artifacts[0].Derivation.DeclaredBatchSizes = []int32{1, 4}
			} else {
				report.ArtifactCapability = &profilingrun.CandidateArtifactCapability{}
				model.Spec.Artifacts[0].Derivation = nil
			}
			reportBytes := encodeCurvePlanCandidateReport(t, report)
			preparer := &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"}
			source, err := NewCurvePlanSource(
				&fakeCurvePlanModelReader{model: model},
				&fakeCurvePlanCandidateResolver{reportBytes: reportBytes}, preparer,
				newFakeCurvePlanStore(), CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
			)
			if err != nil {
				t.Fatal(err)
			}
			plan, _, _, err := source.Resolve(context.Background(), parent)
			if err != nil {
				t.Fatal(err)
			}
			want := []int{1, 2, 4, 8, 16, 32}
			if declared {
				want = []int{1, 4}
			}
			if !slices.Equal(plan.Shared().PlannedBatchSizes, want) ||
				len(preparer.requests) != 2 || preparer.requests[0].Measurement.Batch != 1 ||
				preparer.requests[1].Measurement.Batch != 1 {
				t.Fatalf("plan batches=%v, preparation requests=%+v", plan.Shared().PlannedBatchSizes, preparer.requests)
			}
		})
	}
}

func TestCurvePlanSourceRecordsAndRecoversFrozenPlan(t *testing.T) {
	const storeName = "evidence"

	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	reportBytes, report := curvePlanCandidateReport(t, parent)
	modelReader := &fakeCurvePlanModelReader{model: curvePlanModel(report)}
	modelReader.model.UID = "recreated-model"
	modelReader.model.ResourceVersion = "updated-status"
	resolver := &fakeCurvePlanCandidateResolver{reportBytes: reportBytes}
	preparer := &fakeCurvePlanPreparer{
		report: report, reportBytes: reportBytes, nodeUID: "node-uid",
	}
	store := newFakeCurvePlanStore()
	source, err := NewCurvePlanSource(
		modelReader, resolver, preparer, store,
		CurvePlanSourceOptions{Store: storeName, MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatalf("construct curve-plan source: %v", err)
	}

	first, _, transition, err := source.Resolve(context.Background(), parent)
	if err != nil {
		t.Fatalf("record curve plan: %v", err)
	}
	if transition != nil {
		t.Fatalf("fixed campaign projected transition material: %+v", transition)
	}
	wantBatches := []int{1, 2, 4}
	shared := first.Shared()
	if !slices.Equal(shared.PlannedBatchSizes, wantBatches) ||
		shared.Conversion.Tool != "onnx-tool" || shared.Conversion.ToolVersion != "1.2.3" ||
		shared.Conversion.TargetPlatform != "cpu" {
		t.Fatalf("recorded curve plan = %+v", shared)
	}
	assertFrozenWorkloadStartup(t, shared, parent)
	wantKey := "batch-energy-optimizations/parent-uid/curve-plan.json"
	if store.puts != 1 || store.opens != 1 || store.lastIdentity.Key != wantKey {
		t.Fatalf("first record calls = puts %d, opens %d, identity %+v", store.puts, store.opens, store.lastIdentity)
	}
	storedPayload := store.objects[store.lastIdentity.Location()]
	if len(preparer.requests) != 2 || preparer.requests[0].Measurement.Batch != 1 ||
		preparer.requests[0].Measurement.WorkloadMode != profilingrun.EmpiricalCadenceFixedBatchWorkloadMode ||
		preparer.requests[1].Measurement.Batch != 1 ||
		preparer.requests[1].Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		t.Fatalf("projected preparation requests = %+v", preparer.requests)
	}

	// Recovery returns the stored plan and its observed size without
	// projecting current facts, even when the live projector is unavailable
	// and unrelated facts changed.
	storedPayload = append([]byte(" \n"), storedPayload...)
	store.objects[store.lastIdentity.Location()] = storedPayload
	modelReader.err = errors.New("model reader unavailable")
	modelReader.runs = []edgev1alpha1.ProfilingRun{{ObjectMeta: ownedByCampaign(parent)}}
	preparer.nodeUID = "replacement-node-uid"
	recovered, recoveredReference, recoveredTransition, err := source.Resolve(context.Background(), parent)
	if err != nil {
		t.Fatalf("recover existing curve plan: %v", err)
	}
	if recoveredTransition != nil || !reflect.DeepEqual(recovered.Shared(), first.Shared()) ||
		store.puts != 1 || store.opens != 2 || len(preparer.requests) != 2 ||
		recoveredReference.Store != storeName || recoveredReference.Key != wantKey ||
		recoveredReference.SchemaVersion != first.SchemaVersion() ||
		recoveredReference.Size != int64(len(storedPayload)) {
		t.Fatalf("recovery = reference %+v, puts %d, opens %d, prepares %d",
			recoveredReference, store.puts, store.opens, len(preparer.requests))
	}
}

func TestCurvePlanSourceFreezesWithoutArchiveAndRecoversWorkloadBeforeChildren(t *testing.T) {
	for _, mutation := range []string{"status-only", "slo", "p100", "tuple", "deleted", "recreated"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			config := curveFixtureRunConfig(t)
			payload, err := os.ReadFile("../../profiler/testdata/candidate-report.json")
			if err != nil {
				t.Fatal(err)
			}
			report, err := profilingrun.DecodeCandidateReport(payload)
			if err != nil {
				t.Fatal(err)
			}
			model := curvePlanModel(report)
			model.Spec.Artifacts[0].Derivation = nil
			parent := testParent()
			parent.Spec.State = edgev1alpha1.MeasurementStateSpec{
				TargetNode: config.TargetNode.Name,
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec: config.State.Model, DeviceClass: config.State.DeviceClass,
					ExecutionPath: config.Candidate.ExecutionPath, PowerMode: profilingrun.FixedPowerMode,
					Preprocessor: config.State.Preprocessor.Name,
				},
			}
			parent.Spec.ActiveProtocol.DurationSeconds = 90
			parent.Spec.ActiveProtocol.Dataset = config.Measurement.Dataset.Name
			metric := edgev1alpha1.ProfilingRunMetricObservationSpec{
				IntervalMilliseconds: int32(config.Metrics.IntervalMilliseconds),
				TimeoutMilliseconds:  int32(config.Metrics.TimeoutMilliseconds),
				DurationSeconds:      int32(config.Metrics.DurationSeconds),
			}
			parent.Spec.ActiveProtocol.MetricObservation = metric
			parent.Spec.IdleProtocol.MetricObservation = metric
			workload := &edgev1alpha1.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: parent.Spec.ActiveProtocol.Workload, UID: curvePlanWorkloadUID, ResourceVersion: "7"},
				Spec: edgev1alpha1.WorkloadSpec{
					ModelRef:        edgev1alpha1.CatalogReference{Name: config.State.Model},
					PreprocessorRef: edgev1alpha1.CatalogReference{Name: config.State.Preprocessor.Name},
					DatasetRef:      &edgev1alpha1.CatalogReference{Name: config.Measurement.Dataset.Name},
					SLO:             edgev1alpha1.WorkloadSLO{Percentile: 95, TargetLatency: metav1.Duration{Duration: time.Second}},
				},
			}
			scheme := runtime.NewScheme()
			if err := edgev1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			native := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&edgev1alpha1.Workload{}).
				WithObjects(parent, model, workload).Build()
			workloads, err := profilerkube.NewKubernetesWorkloadObservationResolver(native, profilerkube.WorkloadObservationResolverOptions{
				Namespace: parent.Namespace, ConfigMapName: "unused-image-source", ConfigMapKey: "facts",
			})
			if err != nil {
				t.Fatal(err)
			}
			ports := &frozenWorkloadPreparationPorts{config: config, report: payload, workloads: workloads}
			preparer, err := profiler.NewPreparer(ports, ports, ports, ports, ports, ports)
			if err != nil {
				t.Fatal(err)
			}
			preparer.ResolveLoadOrigin = ports.ResolveLoadOrigin
			recording := &recordingCurvePlanPreparer{preparer: preparer}
			store := newFakeCurvePlanStore()
			options := CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20}
			source, err := NewCurvePlanSource(native, ports, recording, store, options)
			if err != nil {
				t.Fatal(err)
			}
			plan, reference, _, err := source.Resolve(ctx, parent)
			if err != nil {
				t.Fatalf("freeze plan through production preparation: %v", err)
			}
			if len(recording.prepared) != 2 || ports.workloadResolutions != 1 {
				t.Fatalf("initial prepares=%d, Workload resolutions=%d", len(recording.prepared), ports.workloadResolutions)
			}
			original := recording.prepared[0].RunConfig
			mutateFrozenWorkload(t, native, workload, mutation)
			// Restart with a recorded plan and no child Runs. Recovery must pass
			// the frozen source to the real Preparer, not re-read the Workload.
			restarted, err := NewCurvePlanSource(native, ports, recording, store, options)
			if err != nil {
				t.Fatal(err)
			}
			recovered, recoveredReference, transition, err := restarted.Resolve(ctx, parent)
			if err != nil {
				t.Fatalf("recover before children after %s: %v", mutation, err)
			}
			counts := [3]int{len(recording.prepared), ports.workloadResolutions, store.puts}
			if counts != [3]int{3, 1, 1} || transition != nil ||
				reference != recoveredReference || !reflect.DeepEqual(plan.Shared(), recovered.Shared()) {
				t.Fatalf("recovery prepares=%d, Workload resolutions=%d, writes=%d, transition=%+v",
					len(recording.prepared), ports.workloadResolutions, store.puts, transition)
			}
			active := recording.prepared[2].RunConfig
			if active.Measurement.WorkloadUID != curvePlanWorkloadUID || active.Measurement.WorkloadResourceVersion != "7" ||
				active.Measurement.WorkloadName != workload.Name || active.Measurement.SLOPercentile != 95 ||
				active.Measurement.MinServiceSamples != 59 || active.Measurement.SLOTargetLatencyNanos != int64(time.Second) ||
				active.State.Model != original.State.Model || active.State.Preprocessor.Name != original.State.Preprocessor.Name ||
				!reflect.DeepEqual(active.Measurement.Dataset, original.Measurement.Dataset) ||
				!reflect.DeepEqual(active.Actor, original.Actor) {
				t.Fatalf("recovery changed the frozen recipe or Workload contract: %+v", active)
			}
			runs := &edgev1alpha1.ProfilingRunList{}
			if err := native.List(ctx, runs); err != nil || len(runs.Items) != 0 {
				t.Fatalf("pre-child recovery Runs=%+v, err=%v", runs.Items, err)
			}
		})
	}
}

type recordingCurvePlanPreparer struct {
	preparer *profiler.Preparer
	prepared []profiler.PreparedRun
}

func (p *recordingCurvePlanPreparer) Prepare(ctx context.Context, request profiler.PreparationRequest) (profiler.PreparedRun, error) {
	prepared, err := p.preparer.Prepare(ctx, request)
	if err == nil {
		p.prepared = append(p.prepared, prepared)
	}
	return prepared, err
}

func assertFrozenWorkloadStartup(t *testing.T, shared profilederivation.CurvePlanSpec, parent *edgev1alpha1.BatchEnergyOptimization) {
	t.Helper()
	if shared.ActiveProtocol.SLOPercentile != 95 || shared.ActiveProtocol.MinServiceSamples != 59 ||
		shared.ActiveProtocol.SLOTargetLatencyNanos != 1_000_000_000 ||
		shared.ActiveProtocol.WorkloadName != parent.Spec.ActiveProtocol.Workload ||
		shared.ActiveProtocol.WorkloadUID != curvePlanWorkloadUID || shared.ActiveProtocol.WorkloadResourceVersion != "1" {
		t.Fatalf("recorded Workload contract = %+v", shared.ActiveProtocol)
	}
	if shared.Actor.WarmupIterations != int(parent.Spec.ActiveProtocol.WarmupIterations) ||
		shared.ActiveProtocol.StartupWarmupIterations != shared.Actor.WarmupIterations ||
		shared.IdleProtocol.StartupWarmupIterations != shared.Actor.WarmupIterations ||
		!slices.Equal(shared.Actor.WarmupBatchSizes, []int{1}) {
		t.Fatalf("recorded common startup = %+v, active=%+v, idle=%+v",
			shared.Actor, shared.ActiveProtocol, shared.IdleProtocol)
	}
}

func ownedByCampaign(parent *edgev1alpha1.BatchEnergyOptimization) metav1.ObjectMeta {
	return metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
		parent, edgev1alpha1.GroupVersion.WithKind("BatchEnergyOptimization"),
	)}}
}

func TestCurvePlanSourceRecoveryRequiresOwnedRuns(t *testing.T) {
	parent := testParent()
	reportBytes, report := curvePlanCandidateReport(t, parent)
	modelReader := &fakeCurvePlanModelReader{model: curvePlanModel(report)}
	preparer := &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"}
	store := newFakeCurvePlanStore()
	plan, _, err := (&CurvePlanProjector{
		reader:   modelReader,
		resolver: &fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer: preparer,
	}).Project(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store.objects[evidencestore.ObjectLocation{
		Store: "evidence", Key: "batch-energy-optimizations/parent-uid/curve-plan.json",
	}] = payload
	source, err := NewCurvePlanSource(
		modelReader, &fakeCurvePlanCandidateResolver{reportBytes: reportBytes}, preparer, store,
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	foreign := parent.DeepCopy()
	foreign.UID = "stale-parent-uid"
	modelReader.runs = []edgev1alpha1.ProfilingRun{{ObjectMeta: ownedByCampaign(foreign)}}
	if _, _, _, err := source.Resolve(context.Background(), parent); err == nil ||
		!strings.Contains(err.Error(), "ProfilingRun") {
		t.Fatalf("Resolve() error = %v, want stale predecessor Run rejection", err)
	}

	// Before any child exists, only the active transition is prepared for the
	// frozen plan's first batch; the model is not read and no plan is rewritten.
	modelReader.runs = nil
	modelReader.err = errors.New("model reader unavailable")
	prepared := len(preparer.requests)
	if _, _, _, err := source.Resolve(context.Background(), parent); err != nil {
		t.Fatalf("resolve before children exist: %v", err)
	}
	if len(preparer.requests) != prepared+1 ||
		preparer.requests[prepared].Measurement.WorkloadMode != profilingrun.EmpiricalCadenceFixedBatchWorkloadMode ||
		preparer.requests[prepared].Measurement.Batch != plan.PlannedBatchSizes()[0] || store.puts != 0 {
		t.Fatalf("pre-child preparation = %+v, puts %d", preparer.requests[prepared:], store.puts)
	}

	// An owned valid Run means no transition is needed; a measurement-invalid
	// Run is not used, so the transition is prepared again.
	owned := edgev1alpha1.ProfilingRun{ObjectMeta: ownedByCampaign(parent)}
	modelReader.runs = []edgev1alpha1.ProfilingRun{owned}
	prepared = len(preparer.requests)
	if _, _, transition, err := source.Resolve(context.Background(), parent); err != nil || transition != nil || len(preparer.requests) != prepared {
		t.Fatalf("valid owned Run: transition=%+v err=%v", transition, err)
	}
	owned.Annotations = map[string]string{profilingrun.InvalidAnnotation: "PowerChangedDuringMeasurement actuation"}
	modelReader.runs = []edgev1alpha1.ProfilingRun{owned}
	if _, _, transition, err := source.Resolve(context.Background(), parent); err != nil || transition == nil || len(preparer.requests) != prepared+1 {
		t.Fatalf("invalid owned Run: transition=%+v err=%v", transition, err)
	}
}

func TestBindTransitionToPlanRejectsNodeUIDMismatch(t *testing.T) {
	parent := testParent()
	reportBytes, report := curvePlanCandidateReport(t, parent)
	plan, _, err := (&CurvePlanProjector{
		reader:   &fakeCurvePlanModelReader{model: curvePlanModel(report)},
		resolver: &fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer: &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"},
	}).Project(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	material := &TransitionMaterial{Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "other-node-uid"}}
	if _, err := bindTransitionToPlan(plan, material); err == nil {
		t.Fatal("transition for a different Node UID was accepted")
	}
	material.Node.UID = "node-uid"
	if got, err := bindTransitionToPlan(plan, material); err != nil || got != material {
		t.Fatalf("matching transition = %v, %v", got, err)
	}
}

func TestCurvePlanSourceRejectsInvalidStoredPlanWithoutProjecting(t *testing.T) {
	parent := testParent()
	reportBytes, report := curvePlanCandidateReport(t, parent)
	preparer := &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"}
	store := newFakeCurvePlanStore()
	store.objects[evidencestore.ObjectLocation{
		Store: "evidence", Key: "batch-energy-optimizations/parent-uid/curve-plan.json",
	}] = []byte(`{"schemaVersion":"unknown","shared":{}}`)
	source, err := NewCurvePlanSource(
		&fakeCurvePlanModelReader{model: curvePlanModel(report)},
		&fakeCurvePlanCandidateResolver{reportBytes: reportBytes}, preparer, store,
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, _, _, err = source.Resolve(context.Background(), parent)
	if err == nil || errors.Is(err, evidencestore.ErrNotFound) ||
		!strings.Contains(err.Error(), "decode existing curve plan") {
		t.Fatalf("invalid stored plan error = %v", err)
	}
	if store.puts != 0 || len(preparer.requests) != 0 {
		t.Fatalf("invalid stored plan wrote %d objects and prepared %d Runs", store.puts, len(preparer.requests))
	}
}

func TestCurvePlanSourceUsesCanonicalArtifactAndAutomaticBatchDomain(t *testing.T) {
	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	_, report := curvePlanCandidateReport(t, parent)
	report.Selection.Artifact = "canonical-onnx"
	report.ArtifactSupply.Files = []string{"/artifact/canonical.onnx"}
	report.ArtifactCapability = &profilingrun.CandidateArtifactCapability{}
	reportBytes := encodeCurvePlanCandidateReport(t, report)
	model := curvePlanModel(report)
	model.Spec.Artifacts[0].Derivation = nil
	preparer := &fakeCurvePlanPreparer{
		report: report, reportBytes: reportBytes, nodeUID: "node-uid",
	}
	source, err := NewCurvePlanSource(
		&fakeCurvePlanModelReader{model: model},
		&fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer,
		newFakeCurvePlanStore(),
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatalf("construct curve-plan source: %v", err)
	}

	plan, _, _, err := source.Resolve(context.Background(), parent)
	if err != nil {
		t.Fatalf("resolve canonical-artifact curve plan: %v", err)
	}
	shared := plan.Shared()
	if !slices.Equal(shared.PlannedBatchSizes, []int{1, 2, 4, 8, 16, 32}) {
		t.Fatalf("planned batch sizes = %v", shared.PlannedBatchSizes)
	}
	if shared.Conversion.TargetPlatform != "" || shared.Conversion.Tool != "" ||
		shared.Conversion.ToolVersion != "" {
		t.Fatalf("canonical artifact conversion contract = %+v", shared.Conversion)
	}
	if !slices.Equal(shared.Artifact.Files, report.ArtifactSupply.Files) || shared.ActiveProtocol.Dataset == nil {
		t.Fatalf("canonical artifact files or dataset = %+v %+v", shared.Artifact, shared.ActiveProtocol)
	}
	if len(preparer.requests) != 2 || preparer.requests[0].Measurement.Batch != 1 ||
		preparer.requests[1].Measurement.Batch != 1 {
		t.Fatalf("canonical artifact preparation requests = %+v", preparer.requests)
	}
}

func TestCurvePlanSourceRejectsArtifactWithoutBatchOne(t *testing.T) {
	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	_, report := curvePlanCandidateReport(t, parent)
	report.ArtifactCapability.DeclaredBatchSizes = []int32{2, 4}
	reportBytes := encodeCurvePlanCandidateReport(t, report)
	preparer := &fakeCurvePlanPreparer{
		report: report, reportBytes: reportBytes, nodeUID: "node-uid",
	}
	source, err := NewCurvePlanSource(
		&fakeCurvePlanModelReader{model: curvePlanModel(report)},
		&fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer,
		newFakeCurvePlanStore(),
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatalf("construct curve-plan source: %v", err)
	}

	if _, _, _, err := source.Resolve(context.Background(), parent); err == nil ||
		!strings.Contains(err.Error(), "requires artifact capability at batch one") {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(preparer.requests) != 0 {
		t.Fatalf("conflicting sources prepared Runs: %+v", preparer.requests)
	}
}

func TestCurvePlanProjectorPreservesPhaseOrderAndErrorIdentity(t *testing.T) {
	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	reportBytes, report := curvePlanCandidateReport(t, parent)
	calls := []string{}
	modelReader := &fakeCurvePlanModelReader{model: curvePlanModel(report), calls: &calls}
	resolver := &fakeCurvePlanCandidateResolver{reportBytes: reportBytes, calls: &calls}
	preparer := &fakeCurvePlanPreparer{
		report: report, reportBytes: reportBytes, nodeUID: "node-uid", calls: &calls,
	}
	projector, err := NewCurvePlanProjector(modelReader, resolver, preparer)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := projector.Project(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"resolve-candidate",
		"prepare:" + profilingrun.EmpiricalCadenceFixedBatchWorkloadMode,
		"prepare:" + profilingrun.IdleModelLoadedWorkloadMode,
		"read-model",
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("Project() calls = %v, want %v", calls, wantCalls)
	}

	calls = nil
	modelErr := errors.New("model read failed")
	modelReader.err = modelErr
	_, _, err = projector.Project(context.Background(), parent)
	wantError := fmt.Sprintf(
		"get ModelSpec %q for curve-plan artifact: %s", parent.Spec.State.Candidate.ModelSpec, modelErr,
	)
	if !errors.Is(err, modelErr) || err.Error() != wantError {
		t.Fatalf("Project() error = %v, want %q wrapping %q", err, wantError, modelErr)
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("failed Project() calls = %v, want %v", calls, wantCalls)
	}

	calls = nil
	_, _, err = projector.Project(context.Background(), nil)
	if err == nil || err.Error() != "a parent BatchEnergyOptimization is required" {
		t.Fatalf("Project(nil) error = %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("Project(nil) crossed resolver boundary: %v", calls)
	}
}

func curvePlanCandidateReport(
	t *testing.T,
	parent *edgev1alpha1.BatchEnergyOptimization,
) ([]byte, profilingrun.CandidateReport) {
	t.Helper()
	report := profilingrun.CandidateReport{
		SchemaVersion: profilingrun.CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs: profilingrun.CandidateReportInputs{
			ModelSpec: profilingrun.CandidateReportObjectInput{
				Name: parent.Spec.State.Candidate.ModelSpec, UID: "model-uid", ResourceVersion: "1",
			},
			DeviceClass: profilingrun.CandidateReportObjectInput{
				Name: parent.Spec.State.Candidate.DeviceClass, UID: "class-uid", ResourceVersion: "2",
			},
			RuntimeDeclaration: profilingrun.CandidateReportRuntimeInput{
				Namespace: "chill-system", Name: "facts", UID: "facts-uid", ResourceVersion: "3",
				Key: "execution-facts.yaml", Image: "runtime@sha256:aaaaaaaa",
			},
		},
		Selection: profilingrun.CandidateReportSelection{
			ExecutionPath: parent.Spec.State.Candidate.ExecutionPath,
			Artifact:      "derived-onnx",
			PowerMode:     profilingrun.FixedPowerMode,
		},
		DeviceClassFacts: profilingrun.CandidateReportDeviceClassFacts{
			Architecture: "amd64",
			NodeSelector: map[string]string{"edge.dacs.io/device-class": parent.Spec.State.Candidate.DeviceClass},
			PowerModes:   []string{profilingrun.FixedPowerMode},
		},
		ArtifactSupply: profilingrun.CandidateReportArtifactSupply{
			Image: "artifact@sha256:bbbbbbbb", Architecture: "amd64", Files: []string{"/artifact/derived.onnx"},
		},
		ArtifactCapability: &profilingrun.CandidateArtifactCapability{
			TargetPlatform: "cpu", DeclaredBatchSizes: []int32{4, 1, 2},
		},
		Firstfloor:          &modelcompat.FirstfloorSettings{MaxBatchSize: 32, MaxQueueSize: 64},
		MetricObserverImage: "metric@sha256:cccccccc",
		Verdict:             "Compatible",
		Reasons:             []profilingrun.CandidateReportReason{},
	}
	return encodeCurvePlanCandidateReport(t, report), report
}

func encodeCurvePlanCandidateReport(t *testing.T, report profilingrun.CandidateReport) []byte {
	t.Helper()
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profilingrun.DecodeCandidateReport(payload); err != nil {
		t.Fatalf("invalid candidate-report fixture: %v", err)
	}
	return payload
}

func curvePlanModel(report profilingrun.CandidateReport) *edgev1alpha1.ModelSpec {
	return &edgev1alpha1.ModelSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name: report.Inputs.ModelSpec.Name, UID: "model-uid", ResourceVersion: "1",
		},
		Spec: edgev1alpha1.ModelSpecSpec{Artifacts: []edgev1alpha1.ModelArtifact{{
			Name: report.Selection.Artifact, Format: "onnx",
			Derivation: &edgev1alpha1.ModelArtifactDerivation{
				SourceArtifact: "canonical-onnx", TargetPlatform: "cpu",
				DeclaredBatchSizes: []int32{1, 2, 4},
				Conversion:         edgev1alpha1.ModelArtifactConversion{Tool: "onnx-tool", Version: "1.2.3"},
			},
		}}},
	}
}

type fakeCurvePlanModelReader struct {
	model *edgev1alpha1.ModelSpec
	calls *[]string
	err   error
	runs  []edgev1alpha1.ProfilingRun
}

func (r *fakeCurvePlanModelReader) Get(
	_ context.Context,
	key client.ObjectKey,
	object client.Object,
	_ ...client.GetOption,
) error {
	if r.calls != nil {
		*r.calls = append(*r.calls, "read-model")
	}
	if r.err != nil {
		return r.err
	}
	model, ok := object.(*edgev1alpha1.ModelSpec)
	if !ok {
		return fmt.Errorf("unexpected object type %T", object)
	}
	if key.Name != r.model.Name || key.Namespace != "" {
		return fmt.Errorf("unexpected ModelSpec key %s", key)
	}
	r.model.DeepCopyInto(model)
	return nil
}

func (r *fakeCurvePlanModelReader) List(
	_ context.Context,
	list client.ObjectList,
	_ ...client.ListOption,
) error {
	switch typed := list.(type) {
	case *edgev1alpha1.ProfilingRunList:
		typed.Items = r.runs
	default:
		return fmt.Errorf("unexpected List type %T", list)
	}
	return nil
}

type fakeCurvePlanCandidateResolver struct {
	reportBytes []byte
	calls       *[]string
}

func (r *fakeCurvePlanCandidateResolver) ResolveCandidate(
	_ context.Context,
	_ profiler.CandidateSelection,
) (profiler.ResolvedCandidate, error) {
	if r.calls != nil {
		*r.calls = append(*r.calls, "resolve-candidate")
	}
	return profiler.ResolvedCandidate{ReportBytes: append([]byte(nil), r.reportBytes...)}, nil
}

type fakeCurvePlanPreparer struct {
	report      profilingrun.CandidateReport
	reportBytes []byte
	nodeUID     string
	requests    []profiler.PreparationRequest
	calls       *[]string
	err         error
}

func (p *fakeCurvePlanPreparer) Prepare(
	_ context.Context,
	request profiler.PreparationRequest,
) (profiler.PreparedRun, error) {
	p.requests = append(p.requests, request)
	if p.err != nil {
		return profiler.PreparedRun{}, p.err
	}
	if err := request.FreezePhaseDuration(); err != nil {
		return profiler.PreparedRun{}, err
	}
	if p.calls != nil {
		*p.calls = append(*p.calls, "prepare:"+request.Measurement.WorkloadMode)
	}
	artifact := p.report.Selection.Artifact
	config := profilingrun.RunConfig{
		SchemaVersion: profilingrun.RunConfigSchemaVersion,
		Candidate: profilingrun.CandidateIdentity{
			Inputs: p.report.Inputs, ExecutionPath: request.Candidate.ExecutionPath, Verdict: p.report.Verdict,
		},
		State: profilingrun.ExecutionState{
			DeviceClass: request.Candidate.DeviceClass, Model: request.Candidate.ModelSpec,
			Artifact: artifact, ArtifactFiles: p.report.ArtifactSupply.Files,
			RuntimeFamily: "firstfloor", Backend: "ort-cpu", PowerMode: request.Candidate.PowerMode,
			Firstfloor: p.report.Firstfloor,
			Preprocessor: profilingrun.PreprocessorState{
				Name: request.Candidate.Preprocessor, UID: "preprocessor-uid", Image: "preprocessor@sha256:eeeeeeee",
				Architecture: p.report.DeviceClassFacts.Architecture, Path: "/artifact/mobilenet-nchw",
				Inputs: []string{"image"}, Outputs: []string{"label"},
			},
		},
		Images: profilingrun.Images{
			Runtime: p.report.Inputs.RuntimeDeclaration.Image, Artifact: p.report.ArtifactSupply.Image,
			MetricObserver: p.report.MetricObserverImage, WorkloadObserver: "workload@sha256:dddddddd",
		},
		Delivery: profilingrun.CandidateRuntimeDelivery{
			ArtifactCapability: p.report.ArtifactCapability,
		},
		TargetNode: profilingrun.NodeSnapshot{
			Name: request.TargetNode, UID: p.nodeUID, ResourceVersion: "10",
			Architecture: p.report.DeviceClassFacts.Architecture, AllocatableCPU: "4",
		},
		Measurement: request.Measurement,
		Metrics: profilingrun.MetricContract{
			SourceKind: "edge-metrics", NodeName: request.TargetNode,
			Endpoint: "http://central-meter.test:18766/prometheus?hostname=node", PowerMetric: "power_watts",
			MetricLabels:    map[string]string{"hostname": "node", "device_type": "test_device"},
			ThermalEndpoint: "http://edge-metric.monitoring.svc:9102/metrics",
			ThermalMetrics: profilingrun.ThermalMetricContract{
				ObservationAvailable: "thermal_observation_available", Active: "thermal_active",
				CPUCoreThrottleEvents: "thermal_cpu_core_events", CPUPackageThrottleEvents: "thermal_cpu_package_events",
				Latched: "thermal_latched", MaximumTemperatureCelsius: "thermal_max_celsius",
			},
			IntervalMilliseconds: request.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  request.Metrics.TimeoutMilliseconds,
			DurationSeconds:      request.Metrics.DurationSeconds,
		},
		Workload: profilingrun.CurrentWorkloadContract(),
	}
	if config.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		config.LoadOriginNode = "gateway-node"
		config.LoadOriginGatewayPod = &profilingrun.LoadOriginGatewayPodContract{Namespace: "chill-system", Name: "serving-gateway", UID: "gateway-pod-uid"}
		points := *config.Measurement.OperatingPoints
		points.QueueBytesCap = 64 << 20
		config.Measurement.OperatingPoints = &points
		config.Measurement.SLOPercentile = 95
		config.Measurement.MinServiceSamples = 59
		config.Measurement.SLOTargetLatencyNanos = 1_000_000_000
		config.Measurement.WorkloadName = request.Workload
		config.Measurement.WorkloadUID = curvePlanWorkloadUID
		config.Measurement.WorkloadResourceVersion = "1"
	}
	config.Actor = config.ActorConfig()
	config.Actor.MemoryRequestBytes = 128 << 20
	config.Actor.MemoryLimitBytes = 1024 << 20
	if request.Candidate.Dataset != "" {
		config.Measurement.Dataset = &profilingrun.DatasetContract{
			Name: request.Candidate.Dataset, UID: "dataset-uid", Image: "dataset@sha256:ffffffff", Path: "/artifact/dataset",
		}
	}
	if config.State.PowerMode != profilingrun.FixedPowerMode {
		config.Actuation = &profilingrun.ActuationContract{
			PowerMode: config.State.PowerMode,
			Actuator: profilingrun.ActuatorTarget{
				Kind:           profilingrun.ActuatorKindJetsonNVPModel,
				JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 2},
			},
			PolicyName: "policy",
		}
	}
	if err := config.Validate(); err != nil {
		return profiler.PreparedRun{}, fmt.Errorf("invalid prepared config fixture: %w", err)
	}
	prepared := profiler.PreparedRun{RunConfig: config}
	if config.Actuation != nil {
		policy := actuatedPreparedRun(testParent()).ActuationPolicy
		policy.Policy.Name = config.Actuation.PolicyName
		policy.Policy.DeviceClass = config.State.DeviceClass
		policy.Policy.PowerMode = config.State.PowerMode
		prepared.ActuationPolicy = policy
	}
	return prepared, nil
}

type fakeCurvePlanStore struct {
	objects        map[evidencestore.ObjectLocation][]byte
	puts           int
	opens          int
	lastIdentity   evidencestore.ObjectIdentity
	receiptMutator func(evidencestore.Receipt) evidencestore.Receipt
}

func newFakeCurvePlanStore() *fakeCurvePlanStore {
	return &fakeCurvePlanStore{objects: map[evidencestore.ObjectLocation][]byte{}}
}

func (s *fakeCurvePlanStore) PutImmutable(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	s.puts++
	s.lastIdentity = identity
	payload, err := io.ReadAll(content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	if int64(len(payload)) != identity.Size {
		return evidencestore.Receipt{}, evidencestore.ErrContentMismatch
	}
	outcome := evidencestore.CreateOutcomeCreated
	if _, exists := s.objects[identity.Location()]; exists {
		outcome = evidencestore.CreateOutcomeExisting
	} else {
		s.objects[identity.Location()] = bytes.Clone(payload)
	}
	receipt := evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome,
	}
	if s.receiptMutator != nil {
		receipt = s.receiptMutator(receipt)
	}
	return receipt, nil
}

func (s *fakeCurvePlanStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	s.opens++
	payload, exists := s.objects[location]
	if !exists {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}
