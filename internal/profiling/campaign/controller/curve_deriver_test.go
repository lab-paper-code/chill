package controller

import (
	"bytes"
	"context"
	"encoding/json"
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
	profileradmission "github.com/lab-paper-code/chill/internal/profiling/profiler/admission"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCurveDeriverMapsTheSequentialDecision(t *testing.T) {
	fixture := newCurveDeriverFixture(t)

	result, err := fixture.deriver.Derive(
		context.Background(), fixture.plan, fixture.activeRuns, fixture.idleRuns,
		[]int{1, 2}, fixture.policy,
	)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	detail, ok := result.Detail.(profilederivation.SequentialResult)
	if !ok {
		t.Fatalf("Detail type = %T, want profilederivation.SequentialResult", result.Detail)
	}
	wantContinue := detail.Status == profilederivation.SequentialContinue
	if result.Continue != wantContinue {
		t.Fatalf("Continue = %t, want %t from status %q", result.Continue, wantContinue, detail.Status)
	}
	if detail.SelectedBatch == nil || result.SelectedBatch != *detail.SelectedBatch {
		t.Fatalf("SelectedBatch = %d, detail = %+v", result.SelectedBatch, detail.SelectedBatch)
	}
	if detail.Status != profilederivation.SequentialStopped ||
		detail.StopReason != profilederivation.StopNoFurtherImprovement ||
		result.Outcome != "Stopped/NoFurtherImprovement" {
		t.Fatalf("terminal mapping = continue=%t outcome=%q detail=%+v", result.Continue, result.Outcome, detail)
	}
	baseline, exists := detail.IdleBaselinesByState[fixture.stateIdentity]
	if !exists || baseline.PolicyVersion != sequentialPointPolicyVersion ||
		baseline.ConfidenceLevel != sequentialPointConfidenceLevel ||
		len(baseline.Trials) != fixture.policy.TrialsPerBatch {
		t.Fatalf("point policy was not bound to the sequential trial count: %+v", baseline)
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

func TestCurveDeriverRejectsANonAcceptedRunByName(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*curveDeriverFixture) string
	}{
		{
			name: "active",
			mutate: func(fixture *curveDeriverFixture) string {
				runName := fixture.activeRuns[0].Name
				frozen := fixture.intents.byRun[runName]
				frozen.AdmissionPolicy = fixture.insufficientActivePolicy
				fixture.intents.byRun[runName] = frozen
				return runName
			},
		},
		{
			name: "idle",
			mutate: func(fixture *curveDeriverFixture) string {
				runName := fixture.idleRuns[0].Name
				frozen := fixture.intents.byRun[runName]
				frozen.AdmissionPolicy = fixture.insufficientIdlePolicy
				fixture.intents.byRun[runName] = frozen
				return runName
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCurveDeriverFixture(t)
			runName := test.mutate(&fixture)

			_, err := fixture.deriver.Derive(
				context.Background(), fixture.plan, fixture.activeRuns, fixture.idleRuns,
				[]int{1, 2}, fixture.policy,
			)
			if err == nil || !strings.Contains(err.Error(), runName) ||
				!strings.Contains(err.Error(), "Insufficient") {
				t.Fatalf("Derive() error = %v, want named non-Accepted Run", err)
			}
		})
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
	if fixture.intents.calls != 0 || fixture.evidence.calls != 0 {
		t.Fatalf("invalid prefix loaded evidence: intent calls=%d evidence calls=%d",
			fixture.intents.calls, fixture.evidence.calls)
	}
}

func TestValidateCurvePrefixRejectsEmptyBatches(t *testing.T) {
	if err := validateCurvePrefix([]int{1, 2}, nil); err == nil {
		t.Fatal("validateCurvePrefix() accepted an empty batch set")
	}
}

func TestProjectSequentialCurveResultMapsEveryReachableOutcome(t *testing.T) {
	selected := 2
	tests := []struct {
		name         string
		status       profilederivation.SequentialStatus
		reason       profilederivation.SequentialStopReason
		wantContinue bool
		wantOutcome  string
	}{
		{
			name: "continue", status: profilederivation.SequentialContinue,
			wantContinue: true, wantOutcome: "",
		},
		{
			name: "no further improvement", status: profilederivation.SequentialStopped,
			reason: profilederivation.StopNoFurtherImprovement, wantOutcome: "Stopped/NoFurtherImprovement",
		},
		{
			name: "energy divergence", status: profilederivation.SequentialStopped,
			reason: profilederivation.StopEnergyDivergence, wantOutcome: "Stopped/EnergyDivergence",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := profilederivation.SequentialResult{
				Status: test.status, StopReason: test.reason, SelectedBatch: &selected,
			}
			result, err := projectSequentialCurveResult(input)
			if err != nil {
				t.Fatalf("projectSequentialCurveResult() error = %v", err)
			}
			if result.Continue != test.wantContinue || result.SelectedBatch != selected ||
				result.Outcome != test.wantOutcome || !reflect.DeepEqual(result.Detail, input) {
				t.Fatalf("projectSequentialCurveResult() = %+v, detail=%+v", result, result.Detail)
			}
		})
	}
}

func TestProjectSequentialCurveResultRejectsUnreachableCapabilityBoundary(t *testing.T) {
	selected := 2
	_, err := projectSequentialCurveResult(profilederivation.SequentialResult{
		Status:        profilederivation.SequentialStopped,
		StopReason:    profilederivation.StopCapabilityBoundary,
		SelectedBatch: &selected,
	})
	if err == nil || !strings.Contains(err.Error(), "unreachable capability boundary") {
		t.Fatalf("projectSequentialCurveResult() error = %v, want unreachable-state rejection", err)
	}
}

type curveDeriverFixture struct {
	deriver                  *CurveDeriver
	intents                  *fakeCurveIntentLoader
	evidence                 *fakeCurveEvidenceLoader
	plan                     profilederivation.CurvePlan
	activeRuns               []edgev1alpha1.ProfilingRun
	idleRuns                 []edgev1alpha1.ProfilingRun
	policy                   profilederivation.SequentialPolicy
	insufficientActivePolicy profileradmission.FrozenPolicy
	insufficientIdlePolicy   profileradmission.FrozenPolicy
	stateIdentity            string
}

func newCurveDeriverFixture(t *testing.T) curveDeriverFixture {
	t.Helper()
	base := curveFixtureIntent(t)
	idleIntent := base
	idleIntent.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode:   profilingrun.IdleModelLoadedWorkloadMode,
		InputGenerator: profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
	}
	if err := idleIntent.Validate(); err != nil {
		t.Fatal(err)
	}
	capability := profilingrun.CandidateArtifactCapability{
		TargetPlatform: "test", DeclaredBatchSizes: []int32{1, 2},
		InputSampleShape: []int32{3, 224, 224},
	}
	spec, err := profileradmission.ProjectCurvePlanSpec(
		"test-curve", base, idleIntent, capability,
		profilederivation.CurveConversionContract{}, []int{1, 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}

	activePolicy := curveFixturePolicy(t, profileradmission.WallPowerPolicyV2)
	idlePolicy := curveFixturePolicy(t, profileradmission.IdleWallPowerPolicyV1)
	insufficientActivePolicy := curveFixturePolicy(t, "insufficient-active")
	insufficientIdlePolicy := curveFixturePolicy(t, "insufficient-idle")
	intents := &fakeCurveIntentLoader{byRun: map[string]profilercontroller.FrozenRun{}}
	evidence := &fakeCurveEvidenceLoader{
		active: make(map[string]profilerevidence.VerifiedEvidence),
		idle:   make(map[string]profilerevidence.VerifiedIdleEvidence),
	}
	fixture := curveDeriverFixture{
		intents: intents, evidence: evidence, plan: plan,
		insufficientActivePolicy: insufficientActivePolicy,
		insufficientIdlePolicy:   insufficientIdlePolicy,
		policy: profilederivation.SequentialPolicy{
			Version: "sequential-v1", MinDelta: 0.05, Patience: 1,
			TrialsPerBatch: 4, DivergenceFactor: 2,
		},
	}
	for _, batch := range []int{1, 2} {
		watts := float64(4 + 2*batch)
		for trial := 1; trial <= fixture.policy.TrialsPerBatch; trial++ {
			name := fmt.Sprintf("active-b%d-t%d", batch, trial)
			intent := base
			intent.Measurement.Batch = batch
			verified := curveFixtureActiveEvidence(t, intent, name+"-pod", watts)
			fixture.activeRuns = append(fixture.activeRuns, curveFixtureRun(name, intent.Measurement))
			intents.byRun[name] = profilercontroller.FrozenRun{AdmissionPolicy: activePolicy}
			evidence.active[name] = verified
		}
	}
	for trial := 1; trial <= fixture.policy.TrialsPerBatch; trial++ {
		name := fmt.Sprintf("idle-t%d", trial)
		verified := curveFixtureIdleEvidence(t, idleIntent, name+"-pod", 4)
		fixture.idleRuns = append(fixture.idleRuns, curveFixtureRun(name, idleIntent.Measurement))
		intents.byRun[name] = profilercontroller.FrozenRun{AdmissionPolicy: idlePolicy}
		evidence.idle[name] = verified
	}
	firstExecution, _, err := evidence.active[fixture.activeRuns[0].Name].Records()
	if err != nil {
		t.Fatal(err)
	}
	identities, err := profileradmission.ProjectRunIdentities(firstExecution.Intent, firstExecution.Terminal.Node.UID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.stateIdentity = identities.StateIdentity
	fixture.deriver, err = NewCurveDeriver(intents, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

type fakeCurveIntentLoader struct {
	byRun map[string]profilercontroller.FrozenRun
	calls int
}

func (l *fakeCurveIntentLoader) Load(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
) (profilercontroller.FrozenRun, error) {
	l.calls++
	frozen, exists := l.byRun[run.Name]
	if !exists {
		return profilercontroller.FrozenRun{}, fmt.Errorf("unexpected Run %q", run.Name)
	}
	return frozen, nil
}

type fakeCurveEvidenceLoader struct {
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

func curveFixturePolicy(t *testing.T, name string) profileradmission.FrozenPolicy {
	t.Helper()
	catalog := `
schemaVersion: chill.dacs.io/profiler-admission-policies.v1alpha1
policies:
  - name: wall-power-v2
    workloadMode: saturated-serial-fixed-batch
    minimumSamples: 20
    maximumGapSeconds: 1.5
    maximumBoundaryDistanceSeconds: 1
    minimumCoverage: 0.95
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
  - name: idle-wall-power-v1
    workloadMode: idle-model-loaded
    minimumSamples: 20
    maximumGapSeconds: 1.5
    maximumBoundaryDistanceSeconds: 1
    minimumCoverage: 0.95
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
  - name: insufficient-active
    workloadMode: saturated-serial-fixed-batch
    minimumSamples: 31
    maximumGapSeconds: 1.5
    maximumBoundaryDistanceSeconds: 1
    minimumCoverage: 0.95
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
  - name: insufficient-idle
    workloadMode: idle-model-loaded
    minimumSamples: 31
    maximumGapSeconds: 1.5
    maximumBoundaryDistanceSeconds: 1
    minimumCoverage: 0.95
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
`
	policy, err := profileradmission.FreezeConfiguredPolicy(
		[]byte(catalog), name,
		profileradmission.PolicySource{
			Kind: profileradmission.ConfigMapPolicySourceKind, Namespace: "chill-system",
			Name: "policies", UID: "policy-uid", ResourceVersion: "1", Key: "policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return policy
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

func curveFixtureIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	payload, err := os.ReadFile("../../profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	intent.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	intent.Workload = profilingrun.CurrentWorkloadContract()
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func curveFixtureActiveEvidence(
	t *testing.T,
	intent profilingrun.Intent,
	podUID string,
	watts float64,
) profilerevidence.VerifiedEvidence {
	t.Helper()
	raw := curveFixtureActiveRaw(intent, podUID, watts)
	rawBytes, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	execution := curveFixtureExecution(t, intent, podUID, rawBytes)
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
	intent profilingrun.Intent,
	podUID string,
	watts float64,
) profilerevidence.VerifiedIdleEvidence {
	t.Helper()
	activeShape := curveFixtureActiveRaw(intent, podUID, watts)
	started := activeShape.Metrics.StartedAt
	raw := profilerevidence.IdleRawMeasurement{
		SchemaVersion: profilerevidence.IdleRawMeasurementSchemaVersion,
		PodUID:        podUID,
		Runtime: profilingrun.IdleRuntimeResult{
			SchemaVersion: profilingrun.IdleRuntimeResultSchemaVersion, Status: "Succeeded",
			PodUID: podUID, NodeName: intent.TargetNode.Name, Model: intent.State.Model,
			Architecture: "x86_64", Runtime: intent.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Backend: intent.State.Backend, Provider: intent.State.Backend,
			AvailableProviders:        []string{intent.State.Backend},
			ModelLoadCompletedAt:      started.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt:         started.Add(-60 * time.Second).Format(time.RFC3339Nano),
			SettlingCompletedAt:       started.Format(time.RFC3339Nano),
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true,
			CPUContract: curveFixtureRuntimeCPU(intent),
			DerivedContract: profilingrun.DerivedContract{
				Backend: intent.State.Backend, Provider: intent.State.Backend, NodeSnapshot: intent.TargetNode,
			},
		},
		Metrics: activeShape.Metrics, Workload: activeShape.Workload,
	}
	rawBytes, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	execution := curveFixtureExecution(t, intent, podUID, rawBytes)
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
	intent profilingrun.Intent,
	podUID string,
	watts float64,
) profilerevidence.RawMeasurement {
	started := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	targetDuration := float64(intent.Measurement.DurationSeconds)
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
		Phase: "Running", Images: []string{intent.Images.Runtime},
	}
	return profilerevidence.RawMeasurement{
		SchemaVersion: profilerevidence.RawMeasurementSchemaVersion,
		PodUID:        podUID,
		Runtime: profilingrun.RuntimeResult{
			SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded",
			ExperimentID: podUID, NodeName: intent.TargetNode.Name, Model: intent.State.Model,
			Architecture: "x86_64", Runtime: intent.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Backend: intent.State.Backend, Provider: intent.State.Backend,
			AvailableProviders: []string{intent.State.Backend}, BatchSize: intent.Measurement.Batch,
			WarmupIterations:  intent.Measurement.WarmupIterations,
			WarmupCompletedAt: started.Add(-time.Second).Format(time.RFC3339Nano),
			InferenceCount:    2, MeasurementMode: "duration",
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30, TargetDurationSeconds: &targetDuration,
			OutputValidation: profilingrun.OutputValidation{
				Method: intent.Measurement.OutputValidation, Passed: true,
				OutputShapes: [][]int{{intent.Measurement.Batch, 1000}},
			},
			CPUContract: curveFixtureRuntimeCPU(intent),
			DerivedContract: profilingrun.DerivedContract{
				Backend: intent.State.Backend, Provider: intent.State.Backend, NodeSnapshot: intent.TargetNode,
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
				Kind: intent.Metrics.SourceKind, NodeName: intent.Metrics.NodeName,
				Endpoint: intent.Metrics.Endpoint, Metric: intent.Metrics.PowerMetric,
				MetricLabels: intent.Metrics.MetricLabels, ThermalEndpoint: intent.Metrics.ThermalEndpoint,
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
				NodeName: intent.TargetNode.Name, NodeUID: intent.TargetNode.UID,
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

func curveFixtureRuntimeCPU(intent profilingrun.Intent) profilingrun.RuntimeCPU {
	return profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: intent.CPU.ORTIntraOpThreads,
		ORTInterOpThreads: intent.CPU.ORTInterOpThreads, AffinityCPUCount: intent.CPU.ORTIntraOpThreads,
		CPUSetEffective: "0-3", CgroupCPUStatDelta: map[string]int{"user_usec": 1, "system_usec": 2},
	}
}

func curveFixtureExecution(
	t *testing.T,
	intent profilingrun.Intent,
	podUID string,
	rawBytes []byte,
) profilerevidence.ExecutionEvidence {
	t.Helper()
	candidateBytes, err := os.ReadFile("../../profiler/evidence/testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := profilingrun.DecodeCandidateReport(candidateBytes)
	if err != nil {
		t.Fatal(err)
	}
	completed := time.Date(2026, 8, 11, 1, 3, 3, 0, time.UTC)
	container := func(name, image string) profilerevidence.ContainerTerminal {
		return profilerevidence.ContainerTerminal{
			Name: name, RequestedImage: image, ImageID: "containerd://" + name,
			Reason: "Completed", StartedAt: completed.Add(-time.Minute), FinishedAt: completed.Add(-time.Second),
		}
	}
	return profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Intent:        intent, CandidateReport: candidate,
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
				ControllerOwnerUID: "job-" + podUID, NodeName: intent.TargetNode.Name, Phase: "Succeeded",
			},
			Node: profilerevidence.NodeTerminalFacts{Name: intent.TargetNode.Name, UID: intent.TargetNode.UID},
			Containers: []profilerevidence.ContainerTerminal{
				container("metric-observer", intent.Images.MetricObserver),
				container("workload-observer", intent.Images.WorkloadObserver),
				container("runtime", intent.Images.Runtime), container("artifact", intent.Images.Artifact),
			},
			NodeWorkload: profilerevidence.NodeWorkload{
				ObservedAt: completed.Add(time.Second),
				Pods: []profilerevidence.WorkloadPod{{
					Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
					Phase: "Succeeded", Images: []string{intent.Images.Runtime},
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
