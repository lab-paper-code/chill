package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCatalogFollowsFrozenPlanAndRawTrials(t *testing.T) {
	for _, memory := range []uint64{4096, 0} {
		t.Run(fmt.Sprint(memory), func(t *testing.T) {
			catalog, source, objects, planReference := catalogEvidenceFixture(t, memory)
			view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			if memory == 0 {
				if len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || view.Insufficiencies[0].Code != invalidEvidenceCode {
					t.Fatalf("absent required native memory became eligible: %+v", view)
				}
				return
			}
			if len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("same-firstfloor measurements unavailable: %+v", view)
			}
			assertCatalogRawProfile(t, view.Profiles[0])
			// Change only the frozen artifact while keeping the current schema and
			// declared size valid. Structurally valid but mismatching is ineligible.
			plan, err := profilederivation.DecodeCurvePlan(objects[planReference.Location()])
			if err != nil {
				t.Fatal(err)
			}
			spec := plan.Shared()
			spec.Artifact.Name = "different-onnx"
			changed, err := profilederivation.NewCurvePlan(spec)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := changed.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			// Same length makes the failure an artifact binding check, not size.
			if len(payload) != int(planReference.Size) {
				t.Fatal("artifact mutation must preserve payload length")
			}
			objects[planReference.Location()] = payload
			// A changed native source revision opens a new admission boundary;
			// unchanged terminal sources retain their already-verified facts.
			campaign := &edgev1alpha1.BatchEnergyOptimization{}
			if err := catalog.store.client.Get(context.Background(), source, campaign); err != nil {
				t.Fatal(err)
			}
			campaign.Annotations = map[string]string{"catalog-test-revision": "changed"}
			if err := catalog.store.client.Update(context.Background(), campaign); err != nil {
				t.Fatal(err)
			}
			view, err = catalog.Refresh(context.Background(), []string{source.Namespace})
			if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || view.Insufficiencies[0].Code != invalidEvidenceCode {
				t.Fatalf("artifact mismatch remained a candidate: %+v, %v", view, err)
			}
		})
	}
}
func assertCatalogRawProfile(t *testing.T, profile serving.Profile) {
	t.Helper()
	batch := profile.Batches[0]
	if profile.Condition.RuntimeFamily != "firstfloor" || profile.Condition.Artifact.ExecutionPath != "ort-cpu" ||
		profile.NodeName != "lattepanda" || profile.Condition.NodeUID != "uid" || len(profile.Evidence) != 13 ||
		profile.RuntimeMetadata == nil || profile.IdleBaselineWatts != 4 || profile.MeasuredAt.IsZero() ||
		profile.MetricSource.NodeName != "lattepanda" || len(profile.MetricSource.MetricLabels) == 0 ||
		profile.Condition.CPU.Limit != "2" || len(profile.Condition.CPU.AffinityCPUs) != 2 ||
		profile.RuntimeMetadata.IntraOpThreads != 2 || len(profile.RuntimeMetadata.CPUAffinity) != 2 ||
		batch.MemoryBytes != 4096 || len(batch.DurationNanos) != 6 || batch.DurationNanos[0] != 500000 ||
		math.Abs(batch.EnergyPerInputJoules-30) > 1e-9 || batch.EnergyBelowResolution {
		t.Fatalf("frozen identity or measured raw distribution changed: %+v", profile)
	}
}

func TestCatalogNegativeIncrementalEnergyResolution(t *testing.T) {
	for _, tc := range []struct {
		name  string
		watts [6]float64
		below bool
	}{
		{name: "idle uncertainty includes zero", watts: [6]float64{3.9, 3.9, 3.9, 3, 4, 5}, below: true},
		{name: "active uncertainty includes zero", watts: [6]float64{2, 3.9, 5.8, 4, 4, 4}, below: true},
		{name: "joint uncertainty excludes zero", watts: [6]float64{1.99, 2, 2.01, 3.99, 4, 4.01}},
		{name: "exact negative excludes zero", watts: [6]float64{2, 2, 2, 4, 4, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog, source, _, _ := catalogEvidenceFixtureWithPower(t, 4096, tc.watts)
			view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.below {
				if len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 ||
					view.Insufficiencies[0].Code != "NegativeIncrementalEnergy" ||
					!strings.Contains(view.Insufficiencies[0].Reason, "batch 1 incremental energy -30 J/item has 95% CI [") {
					t.Fatalf("resolved negative estimate was not diagnosed with value and CI: %+v", view)
				}
				return
			}
			if len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("below-resolution energy was rejected: %+v", view)
			}
			profile := view.Profiles[0]
			batch := profile.Batches[0]
			if batch.EnergyPerInputJoules != 0 || !batch.EnergyBelowResolution ||
				len(batch.DurationNanos) != 6 || batch.MemoryBytes != 4096 {
				t.Fatalf("below-resolution serving contract changed other measured facts: %+v", batch)
			}
			native := profile.NativeBundle().Batches[0]
			if native.EnergyPerItemJ != 0 {
				t.Fatalf("native bundle lost zero-energy representation: %+v", native)
			}
			bundleJSON, err := json.Marshal(profile.NativeBundle())
			if err != nil || bytes.Contains(bundleJSON, []byte("energyBelowResolution")) {
				t.Fatalf("native bundle changed the frozen runtime schema: %s, %v", bundleJSON, err)
			}
			payload, err := json.Marshal(view)
			if err != nil || !bytes.Contains(payload, []byte(`"energyBelowResolution":true`)) {
				t.Fatalf("catalog API lost below-resolution marker: %s, %v", payload, err)
			}
		})
	}
}

func TestCatalogTwoTrialsReloadAndSelectedConfidence(t *testing.T) {
	for _, confidence := range []string{"", "0.8"} {
		catalog, source, _, _ := catalogEvidenceFixturePolicy(t, 4096, [6]float64{6, 7, 8, 3, 5, 7}, 2, confidence)
		view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
		if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
			t.Fatalf("two nonzero-variance populations failed retained reload: %+v, %v", view, err)
		}
		want := .95
		if confidence != "" {
			want = .8
		}
		profile := view.Profiles[0]
		if profile.ConfidenceLevel != want || profile.TrialsPerBatch != 2 || profile.IdleBaselineWatts != 4 ||
			len(profile.Batches[0].DurationNanos) != 4 || profile.Batches[0].EnergyPerInputJoules != 37.5 {
			t.Fatalf("selected two-trial measurement lost serving values: %+v", profile)
		}
	}
}

func TestCatalogSelectedConfidenceChangesNegativeEnergyDecision(t *testing.T) {
	// At 95% the wide idle uncertainty includes zero; at 10% it excludes zero.
	for _, confidence := range []string{"", "0.1"} {
		catalog, source, _, _ := catalogEvidenceFixturePolicy(t, 4096, [6]float64{3.5, 3.5, 3.5, 3, 4, 5}, 3, confidence)
		view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
		if err != nil {
			t.Fatal(err)
		}
		if confidence == "" {
			if len(view.Profiles) != 1 || !view.Profiles[0].Batches[0].EnergyBelowResolution {
				t.Fatalf("default CI changed: %+v", view)
			}
		} else if len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 ||
			!strings.Contains(view.Insufficiencies[0].Reason, "10% CI") {
			t.Fatalf("selected CI did not affect diagnosis: %+v", view)
		}
	}
}

func TestCatalogRejectsRunAffinityChangedFromCampaignSource(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
	run := &edgev1alpha1.ProfilingRun{}
	ctx := context.Background()
	if err := catalog.store.client.Get(ctx, types.NamespacedName{Namespace: source.Namespace, Name: "trial-0"}, run); err != nil {
		t.Fatal(err)
	}
	run.Spec.Measurement.CPUAffinity = []int32{4, 5, 6, 7}
	if err := catalog.store.client.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 ||
		view.Insufficiencies[0].Code != invalidEvidenceCode || !strings.Contains(view.Insufficiencies[0].Reason, "CPU affinity") {
		t.Fatalf("source CPU selection drift accepted: %+v %v", view, err)
	}
}

type catalogEvidenceObjects map[evidencestore.ObjectLocation][]byte

func (objects catalogEvidenceObjects) OpenLocation(_ context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	payload, exists := objects[location]
	if !exists {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func catalogEvidenceFixture(t *testing.T, memory uint64) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	return catalogEvidenceFixtureWithPower(t, memory, [6]float64{6, 6, 6, 4, 4, 4})
}

func catalogEvidenceFixtureWithPower(t *testing.T, memory uint64, trialWatts [6]float64) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	return catalogEvidenceFixturePolicy(t, memory, trialWatts, 3, "")
}

func catalogEvidenceFixturePolicy(t *testing.T, memory uint64, trialWatts [6]float64, count int, confidence string) (*Catalog, types.NamespacedName, catalogEvidenceObjects, evidencestore.ObjectIdentity) {
	t.Helper()
	payload, err := os.ReadFile("../../profiling/profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var active profilingrun.RunConfig
	if err := json.Unmarshal(payload, &active); err != nil {
		t.Fatal(err)
	}
	active.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 32}
	active.Measurement.DurationToleranceSeconds = .5
	active.CPU.Policy, active.CPU.Limit = profilingrun.ConfiguredCPUQuotaPolicy, "2"
	active.CPU.ORTIntraOpThreads = 2
	active.CPU.AffinityCPUs = []int{0, 1}
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, DurationToleranceSeconds: .5,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode}
	spec, err := profilerprojection.ProjectCurvePlanSpec("catalog-plan", active, idle, profilederivation.CurveConversionContract{}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	objects := catalogEvidenceObjects{}
	put := func(key, schema string, value any) batchenergyoptimization.StoredObjectReference {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		location := evidencestore.ObjectLocation{Store: "evidence", Key: key}
		objects[location] = data
		return batchenergyoptimization.StoredObjectReference{Store: location.Store, Key: key, SchemaVersion: schema, Size: int64(len(data))}
	}
	planBytes, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	planRef := batchenergyoptimization.StoredObjectReference{Store: "evidence", Key: "plan.json", SchemaVersion: plan.SchemaVersion(), Size: int64(len(planBytes))}
	objects[storedObjectIdentity(planRef).Location()] = planBytes
	campaign := &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "catalog-campaign", UID: "campaign-uid", Generation: 1},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate:  edgev1alpha1.ProfilingRunCandidateSpec{ModelSpec: active.Candidate.Inputs.ModelSpec.Name, Preprocessor: active.State.Preprocessor.Name, DeviceClass: active.State.DeviceClass, ExecutionPath: active.Candidate.ExecutionPath, PowerMode: active.State.PowerMode},
				TargetNode: active.TargetNode.Name, CPURequest: resource.MustParse(active.CPU.Request),
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{WarmupIterations: int32(active.Measurement.WarmupIterations), DurationSeconds: 30, Dataset: active.Measurement.Dataset.Name,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31}},
			IdleProtocol: edgev1alpha1.IdleProtocolSpec{SettlingDurationSeconds: 60, DurationSeconds: 30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31}},
			TrialsPerBatch: int32(count),
			PointPolicy:    edgev1alpha1.BatchEnergyOptimizationPointPolicySpec{ConfidenceLevel: confidence},
		},
		Status: edgev1alpha1.BatchEnergyOptimizationStatus{
			Phase: catalogCompletePhase, ObservedGeneration: 1, Outcome: "MinimumFound",
		},
	}
	for _, cpu := range active.CPU.AffinityCPUs {
		campaign.Spec.CPUAffinity = append(campaign.Spec.CPUAffinity, int32(cpu))
	}
	quota := resource.MustParse("2")
	campaign.Spec.CPUQuota = &quota
	apiObjects := []client.Object{campaign}
	completed := make([]batchenergyoptimization.CompletedTrialReference, 0, 6)
	for index := range 6 {
		if index%3 >= count {
			continue
		}
		config, watts, mode := active, trialWatts[index], batchenergyoptimization.WorkloadModeActive
		if index >= 3 {
			config, mode = idle, batchenergyoptimization.WorkloadModeIdle
		}
		name := fmt.Sprintf("trial-%d", index)
		raw, idleRaw, execution := catalogTrialRecords(config, name+"-pod", watts, memory)
		var rawRef batchenergyoptimization.StoredObjectReference
		if mode == batchenergyoptimization.WorkloadModeIdle {
			rawRef = put(name+"-raw", profilerevidence.IdleRawMeasurementSchemaVersion, idleRaw)
		} else {
			rawRef = put(name+"-raw", profilerevidence.RawMeasurementSchemaVersion, raw)
		}
		execution.RawMeasurement = storedObjectIdentity(rawRef)
		executionRef := put(name+"-execution", profilerevidence.ExecutionEvidenceSchemaVersion, execution)
		settling := int32(config.Measurement.SettlingDurationSeconds)
		measurement := edgev1alpha1.ProfilingRunMeasurementSpec{Batch: 1, WarmupIterations: int32(config.Measurement.WarmupIterations), DurationSeconds: 30,
			WorkloadMode: config.Measurement.WorkloadMode}
		if config.Measurement.Dataset != nil {
			measurement.Dataset = config.Measurement.Dataset.Name
		}
		if len(config.CPU.AffinityCPUs) != 0 {
			measurement.CPUAffinity = append([]int32(nil), campaign.Spec.CPUAffinity...)
		}
		if campaign.Spec.CPUQuota != nil {
			quota := campaign.Spec.CPUQuota.DeepCopy()
			measurement.CPUQuota = &quota
		}
		if mode == batchenergyoptimization.WorkloadModeIdle {
			measurement.SettlingDurationSeconds = &settling
		}
		run := &edgev1alpha1.ProfilingRun{
			ObjectMeta: metav1.ObjectMeta{Namespace: campaign.Namespace, Name: name, UID: types.UID(name + "-uid"), Generation: 1,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(campaign, edgev1alpha1.GroupVersion.WithKind("BatchEnergyOptimization"))}},
			Spec: edgev1alpha1.ProfilingRunSpec{MeasurementStateSpec: campaign.Spec.State, Measurement: measurement, MetricObservation: campaign.Spec.ActiveProtocol.MetricObservation},
			Status: edgev1alpha1.ProfilingRunStatus{ObservedGeneration: 1,
				Conditions: []metav1.Condition{{Type: edgev1alpha1.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionTrue, ObservedGeneration: 1},
					{Type: edgev1alpha1.ProfilingRunConditionEvidenceVerified, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
				Job: &edgev1alpha1.ProfilingRunObjectReference{Name: execution.Terminal.Job.Name, UID: execution.Terminal.Job.UID},
				ObservedExecution: &edgev1alpha1.ProfilingRunObservedExecutionStatus{
					Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: execution.Terminal.Pod.Name, UID: execution.Terminal.Pod.UID},
					Node: edgev1alpha1.ProfilingRunObjectReference{Name: config.TargetNode.Name, UID: config.TargetNode.UID}},
				Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{Store: executionRef.Store, Key: executionRef.Key, Size: executionRef.Size, SchemaVersion: executionRef.SchemaVersion}},
		}
		apiObjects = append(apiObjects, run)
		completed = append(completed, batchenergyoptimization.CompletedTrialReference{
			Run:         batchenergyoptimization.RunReference{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)},
			Measurement: batchenergyoptimization.ResultMeasurement{WorkloadMode: mode, Batch: 1}, Evidence: executionRef})
	}
	result, err := batchenergyoptimization.NewResultRecord(batchenergyoptimization.ResultRecordSpec{
		Outcome: "MinimumFound", MeasurementState: batchenergyoptimization.ResultMeasurementState{TargetNode: active.TargetNode.Name,
			ModelSpec: active.Candidate.Inputs.ModelSpec.Name, DeviceClass: active.State.DeviceClass, ExecutionPath: active.Candidate.ExecutionPath, PowerMode: active.State.PowerMode},
		CurvePlan: planRef, CompletedTrials: completed, Detail: []byte(`{"memoryBytes":999999,"latency":0,"energy":0}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := result.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	campaign.Status.Result = &edgev1alpha1.ProfilingRunEvidenceReference{Store: "evidence", Key: "result.json", SchemaVersion: result.SchemaVersion(), Size: int64(len(resultBytes))}
	objects[apiObjectIdentity(campaign.Status.Result).Location()] = resultBytes
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.BatchEnergyOptimization{}).WithObjects(apiObjects...).Build()}
	catalog, err := NewCatalog(store, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}, objects, storedObjectIdentity(planRef)
}

func catalogTrialRecords(config profilingrun.RunConfig, podUID string, watts float64, memory uint64) (profilerevidence.RawMeasurement, profilerevidence.IdleRawMeasurement, profilerevidence.ExecutionEvidence) {
	start := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	end := start.Add(30 * time.Second)
	completed := end.Add(30 * time.Second)
	targetDuration := float64(config.Measurement.DurationSeconds)
	cpu := profilingrun.RuntimeCPU{CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4, ORTInterOpThreads: 1, AffinityCPUCount: 4, AffinityCPUs: []int{0, 1, 2, 3}, CPUSetEffective: "0-3"}
	if config.State.RuntimeFamily == profilingrun.FirstfloorRuntimeFamily {
		cpu.CgroupCPUMax = measuredTwoCoreCPUMax
		cpu.ORTIntraOpThreads = 2
		cpu.AffinityCPUCount = 2
		cpu.AffinityCPUs = []int{0, 1}
	}
	provider := "CPUExecutionProvider"
	derived := profilingrun.DerivedContract{Backend: config.State.Backend, Provider: provider, NodeSnapshot: config.TargetNode}
	pod := workloadobserver.Pod{Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID, Phase: "Running", Images: []string{config.Images.Runtime}}
	thermal := config.Metrics.ThermalMetrics
	metrics := metricobserver.Result{
		Source: metricobserver.SourceIdentity{Kind: config.Metrics.SourceKind, NodeName: config.TargetNode.Name, Endpoint: config.Metrics.Endpoint,
			Metric: config.Metrics.PowerMetric, MetricLabels: config.Metrics.MetricLabels, ThermalEndpoint: config.Metrics.ThermalEndpoint,
			ThermalMetrics: &metricobserver.ThermalMetricIdentity{ObservationAvailable: thermal.ObservationAvailable, Active: thermal.Active,
				CPUCoreThrottleEvents: thermal.CPUCoreThrottleEvents, CPUPackageThrottleEvents: thermal.CPUPackageThrottleEvents, Latched: thermal.Latched,
				MaximumTemperatureCelsius: thermal.MaximumTemperatureCelsius}},
		StartedAt: start, EndedAt: end.Add(time.Second), Samples: []metricobserver.Sample{
			{ObservedAt: start, Watts: watts}, {ObservedAt: end, Watts: watts}},
		Failures: []metricobserver.Failure{}, Summary: metricobserver.Summary{Attempts: 2, SuccessfulSamples: 2, ObservationDurationSeconds: 31, MaximumSampleGapSeconds: 30, SourceTimestampAbsent: true},
	}
	workload := &workloadobserver.Result{SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target:            workloadobserver.TargetIdentity{NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID},
		Run:               workloadobserver.RunIdentity{Namespace: pod.Namespace, JobUID: "job-" + podUID, PodName: pod.Name, PodUID: podUID},
		CoverageStartedAt: start.Add(-time.Second), CoverageEndedAt: end.Add(time.Second), InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{pod}, FinalActivePods: []workloadobserver.Pod{pod}, Continuous: true}
	raw := profilerevidence.RawMeasurement{SchemaVersion: profilerevidence.RawMeasurementSchemaVersion, RunConfig: config, PodUID: podUID, Metrics: metrics, Workload: workload,
		Runtime: profilingrun.RuntimeResult{SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded", ExperimentID: podUID,
			NodeName: config.TargetNode.Name, Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Backend: config.State.Backend, Provider: provider, AvailableProviders: []string{provider}, Inputs: config.State.Preprocessor.Inputs,
			BatchSize: 1, WarmupIterations: config.Measurement.WarmupIterations, WarmupCompletedAt: start.Add(-time.Second).Format(time.RFC3339Nano), InferenceCount: 2,
			MeasurementMode: "duration", MeasurementStartedAt: start.Format(time.RFC3339Nano), MeasurementEndedAt: end.Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30,
			TargetDurationSeconds: &targetDuration, MemoryBytes: memory,
			CPUContract: cpu, DerivedContract: derived, RepetitionResults: []profilingrun.Repetition{{Repetition: 1, MeasurementStartedAt: start.Format(time.RFC3339Nano), MeasurementEndedAt: end.Format(time.RFC3339Nano), LatencyMilliseconds: []float64{1, 2}}}}}
	modelFiles := make([]string, len(config.State.ArtifactFiles))
	for i, file := range config.State.ArtifactFiles {
		modelFiles[i] = "/model/" + filepath.Base(file)
	}
	slices.Sort(modelFiles)
	native := profilingrun.FirstfloorRuntimeEvidence{Mode: "profiling", DeadlinePolicy: firstfloor.DeadlineKeep,
		ModelInstanceCount: 1, MaxBatchSize: 1, MaxQueueSize: 32,
		Runtime: firstfloor.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "1.16.0", ModelFiles: modelFiles, Inputs: config.State.Preprocessor.Inputs, Outputs: config.State.Preprocessor.Outputs,
			Providers: []string{provider}, AvailableProviders: []string{provider}, Device: "cpu", CPUAffinity: []int{0, 1}, IntraOpThreads: 2, InterOpThreads: 1, InitializedBatches: []int{1},
			CgroupCPUMax: measuredTwoCoreCPUMax, CPUSet: "0-3"}}
	activeNative := native
	activeNative.ExecutionCount, activeNative.InferenceCount = 2, 2
	activeNative.BatchExecutionCounts = map[string]uint64{"1": 2}
	raw.Runtime.Firstfloor = &activeNative
	raw.Runtime.MemorySource = "cgroup-v2-memory.peak:runtime-container-lifetime"
	raw.Runtime.RepetitionResults[0].NativeSamples = []profilingrun.NativeServiceSample{
		{BatchID: 1, BatchSize: 1, RecordIDs: []string{"r1"}, Units: []int{1}, RunnerServiceMilliseconds: 0.5, BackendCallMilliseconds: 0.25, NativeRequestMilliseconds: 0.75},
		{BatchID: 2, BatchSize: 1, RecordIDs: []string{"r2"}, Units: []int{1}, RunnerServiceMilliseconds: 1.5, BackendCallMilliseconds: 1, NativeRequestMilliseconds: 1.75},
	}
	idle := profilerevidence.IdleRawMeasurement{SchemaVersion: profilerevidence.IdleRawMeasurementSchemaVersion, RunConfig: config, PodUID: podUID, Metrics: metrics, Workload: workload,
		Runtime: profilingrun.IdleRuntimeResult{SchemaVersion: profilingrun.IdleRuntimeResultSchemaVersion, Status: "Succeeded", PodUID: podUID, NodeName: config.TargetNode.Name,
			Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0", Backend: config.State.Backend, Provider: provider,
			AvailableProviders: []string{provider}, Inputs: config.State.Preprocessor.Inputs, ModelLoadCompletedAt: start.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt: start.Add(-60 * time.Second).Format(time.RFC3339Nano), SettlingCompletedAt: start.Format(time.RFC3339Nano), MeasurementStartedAt: start.Format(time.RFC3339Nano),
			MeasurementEndedAt: end.Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true, CPUContract: cpu, DerivedContract: derived}}
	native.BatchExecutionCounts = map[string]uint64{}
	idle.Runtime.Firstfloor = &native
	idle.Runtime.MemoryBytes, idle.Runtime.MemorySource = memory, "cgroup-v2-memory.peak:runtime-container-lifetime"
	execution := profilerevidence.ExecutionEvidence{SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, RunConfig: config, FinalizedAt: completed.Add(2 * time.Second),
		Terminal: profilerevidence.TerminalFacts{Job: profilerevidence.JobTerminalFacts{Namespace: pod.Namespace, Name: "job-" + podUID, UID: "job-" + podUID, Succeeded: 1, CompletionTime: completed},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: pod.Namespace, Name: pod.Name, UID: podUID, ControllerOwnerUID: "job-" + podUID, NodeName: config.TargetNode.Name, Phase: "Succeeded"},
			Node: profilerevidence.NodeTerminalFacts{Name: config.TargetNode.Name, UID: config.TargetNode.UID}, NodeWorkload: profilerevidence.NodeWorkload{ObservedAt: completed.Add(time.Second),
				Pods: []profilerevidence.WorkloadPod{{Namespace: pod.Namespace, Name: pod.Name, UID: podUID, Phase: "Succeeded", Images: pod.Images}}}}}
	images := map[string]string{"runtime": config.Images.Runtime, "artifact": config.Images.Artifact, "preprocessor": config.State.Preprocessor.Image, "metric-observer": config.Images.MetricObserver, "workload-observer": config.Images.WorkloadObserver}
	if config.Measurement.Dataset != nil {
		images["dataset"] = config.Measurement.Dataset.Image
	}
	for name, image := range images {
		execution.Terminal.Containers = append(execution.Terminal.Containers, profilerevidence.ContainerTerminal{Name: name, RequestedImage: image, ImageID: "containerd://" + name,
			Reason: "Completed", StartedAt: start, FinishedAt: completed.Add(-time.Second)})
	}
	return raw, idle, execution
}

func TestCatalogRetainsAdmittedFactsWithCallerOwnedViews(t *testing.T) {
	const callerMutation = "caller-mutated"
	catalog, source, objects, plan := catalogEvidenceFixture(t, 4096)
	ctx := context.Background()
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("initial admission failed: %+v %v", view, err)
	}
	profile := &view.Profiles[0]
	inputs := slices.Clone(profile.RuntimeMetadata.Inputs)
	preprocessor := profile.Condition.Preprocessor.Inputs[0]
	profile.Evidence[0].Key = callerMutation
	profile.Batches[0].DurationNanos[0] = 999
	profile.Condition.Firstfloor.MaxBatchSize = 999
	profile.Condition.CPU.AffinityCPUs[0] = 999
	profile.Condition.PlannedBatchSizes[0] = 999
	profile.Condition.Preprocessor.Inputs[0] = callerMutation
	profile.RuntimeMetadata.CPUAffinity[0] = 999
	profile.RuntimeMetadata.Inputs[0] = callerMutation
	profile.MetricSource.MetricLabels[callerMutation] = "yes"
	// Previously admitted immutable facts remain usable during backing-object
	// unavailability; current native Campaign metadata must still be read.
	clear(objects)
	for range 2 {
		view, err = catalog.Refresh(ctx, []string{source.Namespace})
		if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
			t.Fatalf("unchanged terminal admission was lost: %+v %v", view, err)
		}
		profile = &view.Profiles[0]
		assertCatalogRawProfile(t, *profile)
		if profile.Evidence[0] != plan || profile.Condition.Firstfloor.MaxBatchSize != 1 ||
			profile.Condition.PlannedBatchSizes[0] != 1 ||
			profile.Condition.Preprocessor.Inputs[0] != preprocessor ||
			!slices.Equal(profile.RuntimeMetadata.Inputs, inputs) ||
			profile.MetricSource.MetricLabels[callerMutation] != "" {
			t.Fatalf("caller mutation reached retained admission: %+v", profile)
		}
		profile.Batches[0].DurationNanos[0] = 999
		profile.Condition.Firstfloor.MaxBatchSize = 999
	}
}

func TestCatalogInvalidatesChangedPendingRemovedAndRecreatedSources(t *testing.T) {
	const recreatedSource = "recreated"
	for _, change := range []string{"version", "pending", "unselected", "deleted", recreatedSource} {
		t.Run(change, func(t *testing.T) {
			catalog, source, objects, _ := catalogEvidenceFixture(t, 4096)
			ctx := context.Background()
			view, err := catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil || len(view.Profiles) != 1 {
				t.Fatalf("initial admission failed: %+v %v", view, err)
			}
			retainedObjects := maps.Clone(objects)
			clear(objects)
			campaign := &edgev1alpha1.BatchEnergyOptimization{}
			if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
				t.Fatal(err)
			}
			// Incomplete and deleted measurements are skipped; changed evidence is read again.
			issues := 1
			switch change {
			case "version":
				campaign.Annotations = map[string]string{"catalog-test-revision": "changed"}
				err = catalog.store.client.Update(ctx, campaign)
			case "pending":
				campaign.Status.Phase = "Measuring"
				err = catalog.store.client.Status().Update(ctx, campaign)
				issues = 0
			case "unselected":
				_, err = catalog.Refresh(ctx, nil)
			case "deleted", recreatedSource:
				err = catalog.store.client.Delete(ctx, campaign)
				if err == nil && change != recreatedSource {
					issues = 0
				}
				if err == nil && change == recreatedSource {
					campaign.UID, campaign.ResourceVersion = "replacement-uid", ""
					err = catalog.store.client.Create(ctx, campaign)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				view, err = catalog.Refresh(ctx, []string{source.Namespace})
				if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != issues {
					t.Fatalf("changed source reused an old success: %+v %v", view, err)
				}
			}
			if change == "deleted" || change == recreatedSource {
				return
			}
			maps.Copy(objects, retainedObjects)
			if change == "pending" {
				if err := catalog.store.client.Get(ctx, source, campaign); err != nil {
					t.Fatal(err)
				}
				campaign.Status.Phase = catalogCompletePhase
				if err := catalog.store.client.Status().Update(ctx, campaign); err != nil {
					t.Fatal(err)
				}
			}
			view, err = catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("fresh evidence did not restore admission: %+v %v", view, err)
			}
			assertCatalogRawProfile(t, view.Profiles[0])
		})
	}
}

// Stored RunConfig objects written before durationToleranceSeconds existed carry
// 0; a Run whose spec never set the field must still be accepted (ADR 0019).
func TestCatalogAcceptsHistoricalRunConfigWithoutDurationTolerance(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
	run := &edgev1alpha1.ProfilingRun{}
	if err := catalog.store.client.Get(context.Background(), types.NamespacedName{Namespace: source.Namespace, Name: "trial-0"}, run); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("../../profiling/profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	config.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 32}
	config.CPU.Policy, config.CPU.Limit = profilingrun.ConfiguredCPUQuotaPolicy, "2"
	config.CPU.ORTIntraOpThreads = 2
	config.CPU.AffinityCPUs = []int{0, 1}
	config.Measurement.DurationToleranceSeconds = 0
	run.Spec.Measurement.DurationToleranceSeconds = nil
	if err := validateCatalogRunConfig(run, config); err != nil && strings.Contains(err.Error(), "Run protocol") {
		t.Fatalf("historical zero tolerance rejected: %v", err)
	}
	explicit := .3
	run.Spec.Measurement.DurationToleranceSeconds = &explicit
	if err := validateCatalogRunConfig(run, config); err == nil || !strings.Contains(err.Error(), "Run protocol") {
		t.Fatalf("explicit spec tolerance mismatch accepted: %v", err)
	}
}
