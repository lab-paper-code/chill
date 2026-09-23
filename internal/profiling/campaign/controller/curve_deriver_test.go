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
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"

	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCurveDeriverMapsTheEnergyObjectiveDecision(t *testing.T) {
	fixture := newCurveDeriverFixture(t)

	result, err := fixture.deriver.Derive(
		context.Background(), fixture.plan, fixture.activeRuns, fixture.idleRuns,
		[]int{1, 2}, fixture.policy,
	)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	detail, ok := result.Detail.(profilederivation.EnergyOptimizationResult)
	if !ok {
		t.Fatalf("Detail type = %T, want profilederivation.EnergyOptimizationResult", result.Detail)
	}
	wantContinue := detail.Status == profilederivation.EnergySearchContinue
	if result.Continue != wantContinue {
		t.Fatalf("Continue = %t, want %t from status %q", result.Continue, wantContinue, detail.Status)
	}
	if detail.BestBatch == nil || *detail.BestBatch != 1 ||
		detail.Status != profilederivation.EnergySearchComplete ||
		detail.CompletionReason != profilederivation.EnergySearchLocalMinimum ||
		result.Outcome != "MinimumFound/LocalMinimum" {
		t.Fatalf("terminal mapping = continue=%t outcome=%q detail=%+v", result.Continue, result.Outcome, detail)
	}
	baseline, exists := detail.IdleBaselinesByState[fixture.stateIdentity]
	if !exists || baseline.PolicyVersion != energyObjectivePointPolicyVersion ||
		baseline.ConfidenceLevel != energyObjectivePointConfidenceLevel ||
		len(baseline.Trials) != fixture.policy.TrialsPerBatch {
		t.Fatalf("point policy was not bound to the energy-objective trial count: %+v", baseline)
	}
}

func TestCurveDeriverUsesParentTrialCountForIdleMinimum(t *testing.T) {
	fixture := newCurveDeriverFixture(t)
	shortIdleRuns := fixture.idleRuns[:fixture.policy.TrialsPerBatch-1]

	_, err := fixture.deriver.Derive(
		context.Background(), fixture.plan, fixture.activeRuns, shortIdleRuns,
		[]int{1, 2}, fixture.policy,
	)
	if err == nil || !strings.Contains(err.Error(), "derive idle baseline") {
		t.Fatalf("Derive() error = %v, want parent-derived idle minimum rejection", err)
	}
}

func TestCurveDeriverRejectsBatchesThatAreNotTheRecordedPrefix(t *testing.T) {
	fixture := newCurveDeriverFixture(t)

	_, err := fixture.deriver.Derive(
		context.Background(), fixture.plan, fixture.activeRuns, fixture.idleRuns,
		[]int{2, 1}, fixture.policy,
	)
	if err == nil || !strings.Contains(err.Error(), "contiguous prefix") {
		t.Fatalf("Derive() error = %v, want recorded-prefix rejection", err)
	}
	if fixture.evidence.calls != 0 {
		t.Fatalf("invalid prefix loaded evidence: evidence calls=%d",
			fixture.evidence.calls)
	}
}

func TestCurveDeriverCollectorsPreserveOrderAndFirstWitness(t *testing.T) {
	fixture := newCurveDeriverFixture(t)
	activeRuns := []edgev1alpha1.ProfilingRun{
		fixture.activeRuns[1], fixture.activeRuns[0], fixture.activeRuns[5], fixture.activeRuns[4],
	}
	passBatches, activeWitness, err := fixture.deriver.collectActiveTrials(
		context.Background(), activeRuns, []int{1, 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(passBatches) != 2 || passBatches[0].Batch != 1 || passBatches[1].Batch != 2 {
		t.Fatalf("pass batches = %+v", passBatches)
	}
	wantActiveTrials := [][]string{
		{"active-b1-t2-pod", "active-b1-t1-pod"},
		{"active-b2-t2-pod", "active-b2-t1-pod"},
	}
	for batchIndex := range passBatches {
		got := make([]string, len(passBatches[batchIndex].Trials))
		for index, admission := range passBatches[batchIndex].Trials {
			got[index] = admission.TrialID
		}
		if !reflect.DeepEqual(got, wantActiveTrials[batchIndex]) {
			t.Fatalf("batch %d trial order = %v, want %v", batchIndex, got, wantActiveTrials[batchIndex])
		}
	}
	activeExecution, _, err := activeWitness.Records()
	if err != nil {
		t.Fatal(err)
	}
	if activeExecution.Terminal.Pod.UID != "active-b1-t2-pod" {
		t.Fatalf("active witness Pod UID = %q", activeExecution.Terminal.Pod.UID)
	}

	idleRuns := []edgev1alpha1.ProfilingRun{fixture.idleRuns[1], fixture.idleRuns[0]}
	idleTrials, idleWitness, err := fixture.deriver.collectIdleTrials(context.Background(), idleRuns)
	if err != nil {
		t.Fatal(err)
	}
	if len(idleTrials) != 2 || idleTrials[0].TrialID != "idle-t2-pod" || idleTrials[1].TrialID != "idle-t1-pod" {
		t.Fatalf("idle trials = %+v", idleTrials)
	}
	idleExecution, _, err := idleWitness.Records()
	if err != nil {
		t.Fatal(err)
	}
	if idleExecution.Terminal.Pod.UID != "idle-t2-pod" {
		t.Fatalf("idle witness Pod UID = %q", idleExecution.Terminal.Pod.UID)
	}
}

func TestCurveDeriverRejectsRunOutsidePrefixBeforeLoading(t *testing.T) {
	fixture := newCurveDeriverFixture(t)
	run := fixture.activeRuns[4]

	_, err := fixture.deriver.Derive(
		context.Background(), fixture.plan, []edgev1alpha1.ProfilingRun{run}, fixture.idleRuns,
		[]int{1}, fixture.policy,
	)
	wantError := fmt.Sprintf(
		"ProfilingRun %q batch %d is outside the requested curve prefix", run.Name, run.Spec.Measurement.Batch,
	)
	if err == nil || err.Error() != wantError {
		t.Fatalf("Derive() error = %v, want %q", err, wantError)
	}
	if fixture.evidence.calls != 0 {
		t.Fatalf(
			"out-of-prefix Run loaded evidence: evidence calls=%d",
			fixture.evidence.calls,
		)
	}
}

func TestCurveDeriverPreservesCollectorLoadErrorIdentity(t *testing.T) {
	fixture := newCurveDeriverFixture(t)
	run := fixture.activeRuns[0]
	loadErr := errors.New("evidence load failed")
	fixture.evidence.err = loadErr

	_, err := fixture.deriver.Derive(
		context.Background(), fixture.plan, fixture.activeRuns, fixture.idleRuns,
		[]int{1, 2}, fixture.policy,
	)
	wantError := fmt.Sprintf("load ProfilingRun %q retained evidence: %s", run.Name, loadErr)
	if !errors.Is(err, loadErr) || err.Error() != wantError {
		t.Fatalf("Derive() error = %v, want %q wrapping %q", err, wantError, loadErr)
	}
	if fixture.evidence.calls != 1 {
		t.Fatalf(
			"load failure calls: evidence=%d", fixture.evidence.calls,
		)
	}
}

func TestValidateCurvePrefixRejectsEmptyBatches(t *testing.T) {
	if err := validateCurvePrefix([]int{1, 2}, nil); err == nil {
		t.Fatal("validateCurvePrefix() completed an empty batch set")
	}
}

func TestProjectEnergyOptimizationResultMapsEveryReachableOutcome(t *testing.T) {
	selected := 2
	tests := []struct {
		name         string
		status       profilederivation.EnergySearchStatus
		reason       profilederivation.EnergySearchCompletionReason
		wantContinue bool
		wantOutcome  string
	}{
		{
			name: "continue", status: profilederivation.EnergySearchContinue,
			wantContinue: true, wantOutcome: "",
		},
		{
			name: "local minimum", status: profilederivation.EnergySearchComplete,
			reason: profilederivation.EnergySearchLocalMinimum, wantOutcome: "MinimumFound/LocalMinimum",
		},
		{
			name: "search space exhausted", status: profilederivation.EnergySearchComplete,
			reason: profilederivation.EnergySearchSpaceExhausted, wantOutcome: "MinimumFound/SearchSpaceExhausted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := profilederivation.EnergyOptimizationResult{
				Status: test.status, CompletionReason: test.reason, BestBatch: &selected,
			}
			result, err := projectEnergyOptimizationResult(input)
			if err != nil {
				t.Fatalf("projectEnergyOptimizationResult() error = %v", err)
			}
			if result.Continue != test.wantContinue ||
				result.Outcome != test.wantOutcome || !reflect.DeepEqual(result.Detail, input) {
				t.Fatalf("projectEnergyOptimizationResult() = %+v, detail=%+v", result, result.Detail)
			}
		})
	}
}

func TestProjectEnergyOptimizationResultRejectsAnUnknownCompletionReason(t *testing.T) {
	selected := 2
	_, err := projectEnergyOptimizationResult(profilederivation.EnergyOptimizationResult{
		Status:           profilederivation.EnergySearchComplete,
		CompletionReason: "unknown",
		BestBatch:        &selected,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported completion reason") {
		t.Fatalf("projectEnergyOptimizationResult() error = %v, want unknown-reason rejection", err)
	}
}

type curveDeriverFixture struct {
	deriver       *CurveDeriver
	evidence      *fakeCurveEvidenceLoader
	plan          profilederivation.CurvePlan
	activeRuns    []edgev1alpha1.ProfilingRun
	idleRuns      []edgev1alpha1.ProfilingRun
	policy        profilederivation.EnergyObjectivePolicy
	stateIdentity string
}

func newCurveDeriverFixture(t *testing.T) curveDeriverFixture {
	t.Helper()
	base := curveFixtureRunConfig(t)
	idleRunConfig := base
	idleRunConfig.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode:   profilingrun.IdleModelLoadedWorkloadMode,
		InputGenerator: profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
	}
	if err := idleRunConfig.Validate(); err != nil {
		t.Fatal(err)
	}
	capability := profilingrun.CandidateArtifactCapability{
		TargetPlatform: "test", DeclaredBatchSizes: []int32{1, 2},
		InputSampleShape: []int32{3, 224, 224},
	}
	spec, err := profilerprojection.ProjectCurvePlanSpec(
		"test-curve", base, idleRunConfig, capability,
		profilederivation.CurveConversionContract{}, []int{1, 2},
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
	fixture := curveDeriverFixture{
		evidence: evidence, plan: plan,
		policy: profilederivation.EnergyObjectivePolicy{
			Version: "quick-energy-v1", TrialsPerBatch: 4,
		},
	}
	for _, batch := range []int{1, 2} {
		watts := float64(4 + 2*batch)
		for trial := 1; trial <= fixture.policy.TrialsPerBatch; trial++ {
			name := fmt.Sprintf("active-b%d-t%d", batch, trial)
			config := base
			config.Measurement.Batch = batch
			verified := curveFixtureActiveEvidence(t, config, name+"-pod", watts)
			fixture.activeRuns = append(fixture.activeRuns, curveFixtureRun(name, config.Measurement))
			evidence.active[name] = verified
		}
	}
	for trial := 1; trial <= fixture.policy.TrialsPerBatch; trial++ {
		name := fmt.Sprintf("idle-t%d", trial)
		verified := curveFixtureIdleEvidence(t, idleRunConfig, name+"-pod", 4)
		fixture.idleRuns = append(fixture.idleRuns, curveFixtureRun(name, idleRunConfig.Measurement))
		evidence.idle[name] = verified
	}
	firstExecution, _, err := evidence.active[fixture.activeRuns[0].Name].Records()
	if err != nil {
		t.Fatal(err)
	}
	identities, err := profilerprojection.ProjectRunIdentities(firstExecution.RunConfig, firstExecution.Terminal.Node.UID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.stateIdentity = identities.StateIdentity
	fixture.deriver, err = NewCurveDeriver(evidence)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

type fakeCurveEvidenceLoader struct {
	err    error
	active map[string]profilerevidence.VerifiedEvidence
	idle   map[string]profilerevidence.VerifiedIdleEvidence
	calls  int
}

func (l *fakeCurveEvidenceLoader) Load(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
	_ evidencestore.ObjectIdentity,
) (profilerevidence.VerifiedEvidence, error) {
	l.calls++
	if l.err != nil {
		return profilerevidence.VerifiedEvidence{}, l.err
	}
	verified, exists := l.active[run.Name]
	if !exists {
		return profilerevidence.VerifiedEvidence{}, fmt.Errorf("unexpected active Run %q", run.Name)
	}
	return verified, nil
}

func (l *fakeCurveEvidenceLoader) LoadIdle(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
	_ evidencestore.ObjectIdentity,
) (profilerevidence.VerifiedIdleEvidence, error) {
	l.calls++
	verified, exists := l.idle[run.Name]
	if !exists {
		return profilerevidence.VerifiedIdleEvidence{}, fmt.Errorf("unexpected idle Run %q", run.Name)
	}
	return verified, nil
}

func curveFixtureRun(name string, measurement profilingrun.MeasurementContract) edgev1alpha1.ProfilingRun {
	return edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "chill-system"},
		Spec: edgev1alpha1.ProfilingRunSpec{Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{
			Batch: int32(measurement.Batch), WorkloadMode: measurement.WorkloadMode,
		}},
		Status: edgev1alpha1.ProfilingRunStatus{Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
			Store: "evidence", Key: name + ".json",
			SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, Size: 1,
		}},
	}
}

type curveFixtureStore map[evidencestore.ObjectLocation][]byte

func (s curveFixtureStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	payload, exists := s[location]
	if !exists {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func curveFixtureRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	payload, err := os.ReadFile("../../profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	config.Workload = profilingrun.CurrentWorkloadContract()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func curveFixtureActiveEvidence(
	t *testing.T,
	config profilingrun.RunConfig,
	podUID string,
	watts float64,
) profilerevidence.VerifiedEvidence {
	t.Helper()
	raw := curveFixtureActiveRaw(config, podUID, watts)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	execution := curveFixtureExecution(t, config, podUID, rawBytes)
	executionBytes, err := execution.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	loader, reference := curveFixtureEvidenceLoader(t, execution, rawBytes, executionBytes)
	verified, err := loader.Load(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func curveFixtureIdleEvidence(
	t *testing.T,
	config profilingrun.RunConfig,
	podUID string,
	watts float64,
) profilerevidence.VerifiedIdleEvidence {
	t.Helper()
	activeShape := curveFixtureActiveRaw(config, podUID, watts)
	started := activeShape.Metrics.StartedAt
	raw := profilerevidence.IdleRawMeasurement{
		SchemaVersion: profilerevidence.IdleRawMeasurementSchemaVersion,
		RunConfig:     config,
		PodUID:        podUID,
		Runtime: profilingrun.IdleRuntimeResult{
			SchemaVersion: profilingrun.IdleRuntimeResultSchemaVersion, Status: "Succeeded",
			PodUID: podUID, NodeName: config.TargetNode.Name, Model: config.State.Model,
			Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Backend: config.State.Backend, Provider: config.State.Backend,
			AvailableProviders:        []string{config.State.Backend},
			ModelLoadCompletedAt:      started.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt:         started.Add(-60 * time.Second).Format(time.RFC3339Nano),
			SettlingCompletedAt:       started.Format(time.RFC3339Nano),
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true,
			CPUContract: curveFixtureRuntimeCPU(config),
			DerivedContract: profilingrun.DerivedContract{
				Backend: config.State.Backend, Provider: config.State.Backend, NodeSnapshot: config.TargetNode,
			},
		},
		Metrics: activeShape.Metrics, Workload: activeShape.Workload,
	}
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	execution := curveFixtureExecution(t, config, podUID, rawBytes)
	executionBytes, err := execution.CanonicalIdleJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	loader, reference := curveFixtureEvidenceLoader(t, execution, rawBytes, executionBytes)
	verified, err := loader.LoadIdle(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func curveFixtureActiveRaw(
	config profilingrun.RunConfig,
	podUID string,
	watts float64,
) profilerevidence.RawMeasurement {
	started := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	targetDuration := float64(config.Measurement.DurationSeconds)
	samples := make([]metricobserver.Sample, 30)
	for index := range samples {
		samples[index] = metricobserver.Sample{
			ObservedAt: started.Add(500*time.Millisecond + time.Duration(index)*time.Second),
			Watts:      watts,
			Thermal: &metricobserver.ThermalReading{
				ObservationAvailable: curveBool(true), Active: curveBool(false),
			},
		}
	}
	workloadPod := workloadobserver.Pod{
		Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
		Phase: "Running", Images: []string{config.Images.Runtime},
	}
	return profilerevidence.RawMeasurement{
		SchemaVersion: profilerevidence.RawMeasurementSchemaVersion,
		RunConfig:     config,
		PodUID:        podUID,
		Runtime: profilingrun.RuntimeResult{
			SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded",
			ExperimentID: podUID, NodeName: config.TargetNode.Name, Model: config.State.Model,
			Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Backend: config.State.Backend, Provider: config.State.Backend,
			AvailableProviders: []string{config.State.Backend}, BatchSize: config.Measurement.Batch,
			WarmupIterations:  config.Measurement.WarmupIterations,
			WarmupCompletedAt: started.Add(-time.Second).Format(time.RFC3339Nano),
			InferenceCount:    2, MeasurementMode: "duration",
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30, TargetDurationSeconds: &targetDuration,
			OutputValidation: profilingrun.OutputValidation{
				Method: config.Measurement.OutputValidation, Passed: true,
				OutputShapes: [][]int{{config.Measurement.Batch, 1000}},
			},
			CPUContract: curveFixtureRuntimeCPU(config),
			DerivedContract: profilingrun.DerivedContract{
				Backend: config.State.Backend, Provider: config.State.Backend, NodeSnapshot: config.TargetNode,
			},
			RepetitionResults: []profilingrun.Repetition{{
				Repetition: 1, MeasurementStartedAt: started.Format(time.RFC3339Nano),
				MeasurementEndedAt:  started.Add(30 * time.Second).Format(time.RFC3339Nano),
				LatencyMilliseconds: []float64{1, 2},
			}},
			Latency: map[string]float64{"p95": 2, "mean": 1.5},
		},
		Metrics: metricobserver.Result{
			Source: metricobserver.SourceIdentity{
				Kind: config.Metrics.SourceKind, NodeName: config.Metrics.NodeName,
				Endpoint: config.Metrics.Endpoint, Metric: config.Metrics.PowerMetric,
				MetricLabels: config.Metrics.MetricLabels, ThermalEndpoint: config.Metrics.ThermalEndpoint,
				ThermalMetrics: &metricobserver.ThermalMetricIdentity{
					ObservationAvailable:      "device_thermal_throttle_observation_available",
					Active:                    "device_thermal_throttle_active",
					CPUCoreThrottleEvents:     "device_thermal_cpu_core_throttle_event_count",
					CPUPackageThrottleEvents:  "device_thermal_cpu_package_throttle_event_count",
					Latched:                   "device_thermal_throttle_latched",
					MaximumTemperatureCelsius: "device_thermal_max_celsius",
				},
			},
			StartedAt: started, EndedAt: started.Add(31 * time.Second), Samples: samples,
			Summary: metricobserver.Summary{
				Attempts: 30, SuccessfulSamples: 30, ObservationDurationSeconds: 31,
				MaximumSampleGapSeconds: 1, SourceTimestampAbsent: true,
			},
		},
		Workload: &workloadobserver.Result{
			SchemaVersion: workloadobserver.ResultSchemaVersion,
			Target: workloadobserver.TargetIdentity{
				NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID,
			},
			Run: workloadobserver.RunIdentity{
				Namespace: "chill-system", JobUID: "job-" + podUID,
				PodName: "profiling-run-pod", PodUID: podUID,
			},
			CoverageStartedAt: started.Add(-time.Second), CoverageEndedAt: started.Add(31 * time.Second),
			InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
			InitialActivePods: []workloadobserver.Pod{workloadPod},
			FinalActivePods:   []workloadobserver.Pod{workloadPod}, Continuous: true,
		},
	}
}

func curveFixtureRuntimeCPU(config profilingrun.RunConfig) profilingrun.RuntimeCPU {
	return profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: config.CPU.ORTIntraOpThreads,
		ORTInterOpThreads: config.CPU.ORTInterOpThreads, AffinityCPUCount: config.CPU.ORTIntraOpThreads,
		CPUSetEffective: "0-3", CgroupCPUStatDelta: map[string]int{"user_usec": 1, "system_usec": 2},
	}
}

func curveFixtureExecution(
	t *testing.T,
	config profilingrun.RunConfig,
	podUID string,
	rawBytes []byte,
) profilerevidence.ExecutionEvidence {
	t.Helper()
	completed := time.Date(2026, 8, 11, 1, 3, 3, 0, time.UTC)
	container := func(name, image string) profilerevidence.ContainerTerminal {
		return profilerevidence.ContainerTerminal{
			Name: name, RequestedImage: image, ImageID: "containerd://" + name,
			Reason: "Completed", StartedAt: completed.Add(-time.Minute), FinishedAt: completed.Add(-time.Second),
		}
	}
	return profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		RunConfig:     config,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store: "evidence", Key: "raw-" + podUID + ".json", Size: int64(len(rawBytes)),
		},
		Terminal: profilerevidence.TerminalFacts{
			Job: profilerevidence.JobTerminalFacts{
				Namespace: "chill-system", Name: "job-" + podUID, UID: "job-" + podUID,
				Succeeded: 1, CompletionTime: completed,
			},
			Pod: profilerevidence.PodTerminalFacts{
				Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
				ControllerOwnerUID: "job-" + podUID, NodeName: config.TargetNode.Name, Phase: "Succeeded",
			},
			Node: profilerevidence.NodeTerminalFacts{Name: config.TargetNode.Name, UID: config.TargetNode.UID},
			Containers: []profilerevidence.ContainerTerminal{
				container("metric-observer", config.Images.MetricObserver),
				container("workload-observer", config.Images.WorkloadObserver),
				container("runtime", config.Images.Runtime), container("artifact", config.Images.Artifact),
			},
			NodeWorkload: profilerevidence.NodeWorkload{
				ObservedAt: completed.Add(time.Second),
				Pods: []profilerevidence.WorkloadPod{{
					Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
					Phase: "Succeeded", Images: []string{config.Images.Runtime},
				}},
			},
		},
		FinalizedAt: completed.Add(2 * time.Second),
	}
}

func curveFixtureEvidenceLoader(
	t *testing.T,
	execution profilerevidence.ExecutionEvidence,
	rawBytes, executionBytes []byte,
) (*profilerevidence.Loader, evidencestore.ObjectIdentity) {
	t.Helper()
	reference := evidencestore.ObjectIdentity{
		Store: "evidence", Key: "execution-" + execution.Terminal.Pod.UID + ".json",
		Size: int64(len(executionBytes)),
	}
	loader, err := profilerevidence.NewLoader(curveFixtureStore{
		{Store: execution.RawMeasurement.Store, Key: execution.RawMeasurement.Key}: rawBytes,
		{Store: reference.Store, Key: reference.Key}:                               executionBytes,
	}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return loader, reference
}

func curveBool(value bool) *bool { return &value }
