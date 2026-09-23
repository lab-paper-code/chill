package evidence

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestRawMeasurementCanonicalJSONAndValidation(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)

	first, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decodedRaw RawMeasurement
	if err := json.Unmarshal(first, &decodedRaw); err != nil {
		t.Fatal(err)
	}
	if err := decodedRaw.Validate(); err != nil {
		t.Fatal(err)
	}
	if raw.Metrics.StartedAt.Location() == time.UTC {
		t.Fatal("canonical encoding mutated the caller's power timestamps")
	}

	drifted := raw
	drifted.Metrics.Source.NodeName = "other"
	if err := drifted.Validate(); err == nil {
		t.Fatal("expected metric source identity drift rejection")
	}

	drifted = raw
	drifted.Metrics.Summary.Attempts++
	if err := drifted.Validate(); err == nil {
		t.Fatal("expected inconsistent metric summary rejection")
	}

	drifted = raw
	drifted.Metrics.Source.ThermalMetrics = nil
	if err := drifted.Validate(); err == nil {
		t.Fatal("expected missing thermal metric identity rejection")
	}

	drifted = raw
	thermal := *raw.Metrics.Source.ThermalMetrics
	thermal.Active = "other_thermal_active"
	drifted.Metrics.Source.ThermalMetrics = &thermal
	if err := drifted.Validate(); err == nil {
		t.Fatal("expected thermal metric identity drift rejection")
	}
}

func TestCompositeMetricSourcePinsCurrentRawSchemas(t *testing.T) {
	if RawMeasurementSchemaVersion != "chill.dacs.io/profiling-raw-measurement.v1alpha7" ||
		IdleRawMeasurementSchemaVersion != "chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha5" {
		t.Fatalf("raw schemas changed: active=%q idle=%q", RawMeasurementSchemaVersion, IdleRawMeasurementSchemaVersion)
	}
	config := loadRunConfig(t)
	active := validRaw(t, config)
	active.SchemaVersion = "chill.dacs.io/profiling-raw-measurement.v1alpha5"
	if err := active.Validate(); err == nil {
		t.Fatal("retired active raw schema was accepted")
	}
	idle := validIdleRaw(t, config)
	idle.SchemaVersion = "chill.dacs.io/model-loaded-idle-raw-measurement.v1alpha3"
	if err := idle.Validate(); err == nil {
		t.Fatal("retired idle raw schema was accepted")
	}
}

func TestVerifiedRawCompositionMatchesValidatedCanonicalForms(t *testing.T) {
	t.Run("active", func(t *testing.T) {
		config := loadRunConfig(t)
		raw := validRaw(t, config)
		verifiedRunConfig := verifyRunConfigForTest(t, config)
		verifiedRuntime, err := profilingrun.VerifyRuntimeResult(verifiedRunConfig, raw.PodUID, raw.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		verifiedMetrics, err := metricobserver.VerifyResult(raw.Metrics)
		if err != nil {
			t.Fatal(err)
		}
		verifiedWorkload, err := workloadobserver.VerifyResult(*raw.Workload)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ComposeRawMeasurement(
			verifiedRunConfig, raw.PodUID, verifiedRuntime, verifiedMetrics, verifiedWorkload,
		)
		if err != nil {
			t.Fatal(err)
		}
		var composed RawMeasurement
		if err := json.Unmarshal(got, &composed); err != nil {
			t.Fatal(err)
		}
		if err := composed.Validate(); err != nil {
			t.Fatalf("verified active composition is invalid: %v", err)
		}

		driftedMetrics := raw.Metrics
		driftedMetrics.Source.NodeName = "other-node"
		verifiedDrift, err := metricobserver.VerifyResult(driftedMetrics)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ComposeRawMeasurement(
			verifiedRunConfig, raw.PodUID, verifiedRuntime, verifiedDrift, verifiedWorkload,
		); err == nil || !strings.Contains(err.Error(), "source identity") {
			t.Fatalf("expected verified cross-record drift rejection, got %v", err)
		}

		driftedMetrics = raw.Metrics
		thermal := *raw.Metrics.Source.ThermalMetrics
		thermal.Active = "other_thermal_active"
		driftedMetrics.Source.ThermalMetrics = &thermal
		verifiedDrift, err = metricobserver.VerifyResult(driftedMetrics)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ComposeRawMeasurement(
			verifiedRunConfig, raw.PodUID, verifiedRuntime, verifiedDrift, verifiedWorkload,
		); err == nil || !strings.Contains(err.Error(), "source identity") {
			t.Fatalf("expected verified thermal-identity drift rejection, got %v", err)
		}
	})

	t.Run("idle", func(t *testing.T) {
		config := idleRunConfig(t)
		raw := validIdleRaw(t, config)
		verifiedRunConfig := verifyRunConfigForTest(t, config)
		verifiedRuntime, err := profilingrun.VerifyIdleRuntimeResult(verifiedRunConfig, raw.PodUID, raw.Runtime)
		if err != nil {
			t.Fatal(err)
		}
		verifiedMetrics, err := metricobserver.VerifyResult(raw.Metrics)
		if err != nil {
			t.Fatal(err)
		}
		verifiedWorkload, err := workloadobserver.VerifyResult(*raw.Workload)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ComposeIdleRawMeasurement(
			verifiedRunConfig, raw.PodUID, verifiedRuntime, verifiedMetrics, verifiedWorkload,
		)
		if err != nil {
			t.Fatal(err)
		}
		var composed IdleRawMeasurement
		if err := json.Unmarshal(got, &composed); err != nil {
			t.Fatal(err)
		}
		if err := composed.Validate(); err != nil {
			t.Fatalf("verified idle composition is invalid: %v", err)
		}
	})
}

func verifyRunConfigForTest(t *testing.T, config profilingrun.RunConfig) profilingrun.VerifiedRunConfig {
	t.Helper()
	verified, err := profilingrun.ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func TestRawMeasurementBindsCompositeMetricSourceToRunConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*metricobserver.SourceIdentity)
	}{
		{name: "labels", mutate: func(source *metricobserver.SourceIdentity) {
			source.MetricLabels = map[string]string{"hostname": "other", "device_type": "test_device"}
		}},
		{name: "thermal endpoint", mutate: func(source *metricobserver.SourceIdentity) {
			source.ThermalEndpoint = "http://other-thermal.test:9102/metrics"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := loadRunConfig(t)
			raw := validRaw(t, config)
			test.mutate(&raw.Metrics.Source)
			if err := raw.Validate(); err == nil || !strings.Contains(err.Error(), "metric source identity mismatch") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestActuatedExecutionEvidenceBindsIndependentTargetReadBack(t *testing.T) {
	t.Parallel()
	const expectedSchemaVersion = "chill.dacs.io/profiling-execution-evidence.v1alpha14"
	if ExecutionEvidenceSchemaVersion != expectedSchemaVersion {
		t.Fatalf("execution evidence schemaVersion = %q, want %q", ExecutionEvidenceSchemaVersion, expectedSchemaVersion)
	}
	config, raw, execution := validActuatedExecutionEvidence(t)
	payload, err := execution.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExecutionEvidence
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != ExecutionEvidenceSchemaVersion || decoded.Actuation == nil ||
		decoded.Actuation.Handoff.UID != execution.Actuation.Handoff.UID ||
		decoded.Actuation.Target.PowerMode != config.Actuation.PowerMode {
		t.Fatalf("actuated evidence round trip = %#v", decoded.Actuation)
	}
	if strings.Contains(string(payload), `"previous"`) {
		t.Fatalf("Run evidence retained transition-owned previous state: %s", payload)
	}

	tests := []struct {
		name      string
		mutate    func(*ExecutionEvidence)
		match     string
		permanent bool
	}{
		{name: "retired same-shape schema", mutate: func(candidate *ExecutionEvidence) {
			candidate.SchemaVersion = "chill.dacs.io/profiling-execution-evidence.v1alpha4"
		}, match: "unsupported execution evidence schemaVersion"},
		{name: "retired predecessor schema", mutate: func(candidate *ExecutionEvidence) {
			candidate.SchemaVersion = "chill.dacs.io/profiling-execution-evidence.v1alpha12"
		}, match: "unsupported execution evidence schemaVersion"},
		{name: "missing actuation", mutate: func(candidate *ExecutionEvidence) { candidate.Actuation = nil }, permanent: true},
		{name: "target drift", mutate: func(candidate *ExecutionEvidence) {
			candidate.Actuation.Target.Actuator.JetsonNVPModel.ID++
		}, permanent: true},
		{name: "container before ready", mutate: func(candidate *ExecutionEvidence) {
			candidate.Actuation.ReadyAt = candidate.Terminal.Containers[0].StartedAt.Add(time.Second)
		}, permanent: true},
		{name: "ready before target read-back", mutate: func(candidate *ExecutionEvidence) {
			candidate.Actuation.ReadyAt = candidate.Actuation.Target.ObservedAt.Add(-time.Second)
		}, permanent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := execution
			candidate.Actuation = cloneActuationFactsForTest(execution.Actuation)
			test.mutate(&candidate)
			err := candidate.Validate(raw)
			if err == nil {
				t.Fatalf("%s drift was accepted", test.name)
			}
			if test.match != "" && !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate() error = %q, want %q", err, test.match)
			}
			if permanent := errors.Is(err, ErrPermanentValidation); permanent != test.permanent {
				t.Fatalf("Validate() permanent = %t, want %t: %v", permanent, test.permanent, err)
			}
		})
	}
}

func TestFixedExecutionEvidenceRejectsActuationFacts(t *testing.T) {
	t.Parallel()
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	execution := validExecutionEvidence(t, config, raw)
	_, _, facts := validActuatedExecutionEvidence(t)
	execution.Actuation = facts.Actuation
	if err := execution.Validate(raw); err == nil {
		t.Fatal("fixed evidence accepted actuation facts")
	} else if !errors.Is(err, ErrPermanentValidation) {
		t.Fatalf("fixed evidence error = %v, want permanent validation", err)
	}
}

func TestRawMeasurementRequiresOverlappingRuntimeAndMetricWindows(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	runtimeStarted, runtimeEnded, err := raw.Runtime.MeasurementWindow()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		powerStart time.Time
		powerEnd   time.Time
		wantError  bool
	}{
		{
			name:       "partial overlap retains observed facts",
			powerStart: runtimeStarted.Add(time.Second), powerEnd: runtimeEnded.Add(time.Second),
		},
		{
			name:       "power ends at runtime start",
			powerStart: runtimeStarted.Add(-time.Minute), powerEnd: runtimeStarted, wantError: true,
		},
		{
			name:       "power starts at runtime end",
			powerStart: runtimeEnded, powerEnd: runtimeEnded.Add(time.Minute), wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := raw
			candidate.Metrics.StartedAt = test.powerStart
			candidate.Metrics.EndedAt = test.powerEnd
			candidate.Metrics.Samples = nil
			candidate.Metrics.Summary.Attempts = 0
			candidate.Metrics.Summary.SuccessfulSamples = 0
			candidate.Metrics.Summary.ObservationDurationSeconds = test.powerEnd.Sub(test.powerStart).Seconds()
			err := candidate.Validate()
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestCurrentRawMeasurementBindsWorkloadObservationToRunConfigAndRuntime(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	runtimeStarted, _, err := raw.Runtime.MeasurementWindow()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*RawMeasurement)
		match  string
	}{
		{name: "missing", mutate: func(candidate *RawMeasurement) {
			candidate.Workload = nil
		}, match: "required"},
		{name: "pod", mutate: func(candidate *RawMeasurement) {
			candidate.Workload.Run.PodUID = "other-pod"
		}, match: "Run Pod"},
		{name: "node", mutate: func(candidate *RawMeasurement) {
			candidate.Workload.Target.NodeUID = "other-node"
		}, match: "identity"},
		{name: "coverage", mutate: func(candidate *RawMeasurement) {
			candidate.Workload.CoverageStartedAt = runtimeStarted.Add(time.Second)
		}, match: "does not cover"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := raw
			workload := *raw.Workload
			candidate.Workload = &workload
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestIdleRawMeasurementBindsRuntimeDeclaredWindowAndObservers(t *testing.T) {
	config := idleRunConfig(t)
	raw := validIdleRaw(t, config)
	first, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded IdleRawMeasurement
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*IdleRawMeasurement)
	}{
		{name: "inference", mutate: func(candidate *IdleRawMeasurement) { candidate.Runtime.InferenceCountAfterModelLoad = 1 }},
		{name: "residency", mutate: func(candidate *IdleRawMeasurement) { candidate.Runtime.ModelResidentAtEnd = false }},
		{name: "metric identity", mutate: func(candidate *IdleRawMeasurement) { candidate.Metrics.Source.NodeName = "other" }},
		{name: "workload coverage", mutate: func(candidate *IdleRawMeasurement) {
			workload := *candidate.Workload
			candidate.Workload = &workload
			started, _, windowErr := candidate.Runtime.MeasurementWindow()
			if windowErr != nil {
				t.Fatal(windowErr)
			}
			candidate.Workload.CoverageStartedAt = started.Add(time.Second)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := raw
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid idle raw measurement was accepted")
			}
		})
	}
}

func TestIdleRawMeasurementStrictDecodeRejectsUnknownFields(t *testing.T) {
	config := idleRunConfig(t)
	payload, err := validIdleRaw(t, config).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.Replace(string(payload), `"podUID":`, `"unknown":true,"podUID":`, 1))
	var decoded IdleRawMeasurement
	if err := json.Unmarshal(payload, &decoded); err == nil {
		t.Fatal("idle raw measurement accepted an unknown field")
	}
}

func TestExecutionEvidenceCanonicalJSONAndIdentityLink(t *testing.T) {
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	evidence := validExecutionEvidence(t, config, raw)

	first, err := evidence.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExecutionEvidence
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(raw); err != nil {
		t.Fatal(err)
	}

	invalid := evidence
	invalid.RawMeasurement.Key = ""
	if err := invalid.Validate(raw); err == nil {
		t.Fatal("expected malformed raw object identity rejection")
	}

	invalid = evidence
	invalid.Terminal.Pod.ControllerOwnerUID = "other-job"
	if err := invalid.Validate(raw); err == nil {
		t.Fatal("expected Job-to-Pod ownership rejection")
	}

	driftedRaw := raw
	workload := *raw.Workload
	driftedRaw.Workload = &workload
	driftedRaw.Workload.Run.JobUID = "other-job"
	if err := evidence.Validate(driftedRaw); err == nil || !strings.Contains(err.Error(), "terminal Kubernetes") {
		t.Fatalf("expected workload-to-terminal identity rejection, got %v", err)
	}

	invalid = evidence
	invalid.Terminal.NodeWorkload.Pods = invalid.Terminal.NodeWorkload.Pods[:1]
	if err := invalid.Validate(raw); err == nil {
		t.Fatal("expected missing Run Pod workload observation rejection")
	}
}

func TestExecutionEvidenceValidatesIdleRawObjectIdentity(t *testing.T) {
	config := idleRunConfig(t)
	raw := validIdleRaw(t, config)
	execution := validIdleExecutionEvidence(t, config, raw)
	payload, err := execution.CanonicalIdleJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExecutionEvidence
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.ValidateIdle(raw); err != nil {
		t.Fatal(err)
	}
	invalid := execution
	invalid.RawMeasurement.Key = ""
	if err := invalid.ValidateIdle(raw); err == nil {
		t.Fatal("idle execution evidence accepted a malformed raw object identity")
	}
}

func loadRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
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

func validRaw(t *testing.T, config profilingrun.RunConfig) RawMeasurement {
	t.Helper()
	location := time.FixedZone("KST", 9*60*60)
	started := time.Date(2026, time.July, 16, 12, 0, 0, 0, location)
	podUID := "profiling-pod-uid"
	targetDuration := float64(config.Measurement.DurationSeconds)
	raw := RawMeasurement{
		SchemaVersion: RawMeasurementSchemaVersion,
		RunConfig:     config,
		PodUID:        podUID,
		Runtime: profilingrun.RuntimeResult{
			SchemaVersion:             profilingrun.RuntimeResultSchemaVersion,
			Status:                    "Succeeded",
			ExperimentID:              podUID,
			NodeName:                  config.TargetNode.Name,
			Model:                     config.State.Model,
			Architecture:              "x86_64",
			Runtime:                   config.State.RuntimeFamily,
			RuntimeVersion:            "1.16.0",
			Backend:                   config.State.Backend,
			Provider:                  config.State.Backend,
			AvailableProviders:        []string{config.State.Backend},
			BatchSize:                 config.Measurement.Batch,
			WarmupIterations:          config.Measurement.WarmupIterations,
			WarmupCompletedAt:         started.Add(-time.Second).UTC().Format(time.RFC3339Nano),
			InferenceCount:            2,
			MeasurementMode:           "duration",
			MeasurementStartedAt:      started.UTC().Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).UTC().Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30,
			TargetDurationSeconds:     &targetDuration,
			OutputValidation: profilingrun.OutputValidation{
				Method:       config.Measurement.OutputValidation,
				Passed:       true,
				OutputShapes: [][]int{{config.Measurement.Batch, 1000}},
			},
			CPUContract: profilingrun.RuntimeCPU{
				CgroupCPUMax:       "400000 100000",
				ORTIntraOpThreads:  config.CPU.ORTIntraOpThreads,
				ORTInterOpThreads:  config.CPU.ORTInterOpThreads,
				AffinityCPUCount:   config.CPU.ORTIntraOpThreads,
				CPUSetEffective:    "0-3",
				CgroupCPUStatDelta: map[string]int{"user_usec": 1, "system_usec": 2},
			},
			DerivedContract: profilingrun.DerivedContract{
				Backend:      config.State.Backend,
				Provider:     config.State.Backend,
				NodeSnapshot: config.TargetNode,
			},
			RepetitionResults: []profilingrun.Repetition{{
				Repetition:           1,
				MeasurementStartedAt: started.UTC().Format(time.RFC3339Nano),
				MeasurementEndedAt:   started.Add(30 * time.Second).UTC().Format(time.RFC3339Nano),
				LatencyMilliseconds:  []float64{1, 2},
			}},
			Latency: map[string]float64{"p95": 2, "mean": 1.5},
		},
		Metrics: metricobserver.Result{
			Source: metricobserver.SourceIdentity{
				Kind:            config.Metrics.SourceKind,
				NodeName:        config.Metrics.NodeName,
				Endpoint:        config.Metrics.Endpoint,
				Metric:          config.Metrics.PowerMetric,
				MetricLabels:    config.Metrics.MetricLabels,
				ThermalEndpoint: config.Metrics.ThermalEndpoint,
				ThermalMetrics: &metricobserver.ThermalMetricIdentity{
					ObservationAvailable:      config.Metrics.ThermalMetrics.ObservationAvailable,
					Active:                    config.Metrics.ThermalMetrics.Active,
					CPUCoreThrottleEvents:     config.Metrics.ThermalMetrics.CPUCoreThrottleEvents,
					CPUPackageThrottleEvents:  config.Metrics.ThermalMetrics.CPUPackageThrottleEvents,
					Latched:                   config.Metrics.ThermalMetrics.Latched,
					MaximumTemperatureCelsius: config.Metrics.ThermalMetrics.MaximumTemperatureCelsius,
				},
			},
			StartedAt: started,
			EndedAt:   started.Add(31 * time.Second),
			Samples: []metricobserver.Sample{{
				ObservedAt: started.Add(time.Second),
				Watts:      5.2,
			}},
			Failures: []metricobserver.Failure{},
			Summary: metricobserver.Summary{
				Attempts:                   1,
				SuccessfulSamples:          1,
				Failures:                   0,
				ObservationDurationSeconds: 31,
				SourceTimestampAbsent:      true,
			},
		},
	}
	runPod := workloadobserver.Pod{
		Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID, Phase: "Running",
		Images: []string{config.Images.Runtime},
	}
	raw.Workload = &workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID,
		},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system", JobUID: "job-uid", PodName: "profiling-run-pod", PodUID: podUID,
		},
		CoverageStartedAt: started.Add(-time.Second), CoverageEndedAt: started.Add(31 * time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{runPod}, FinalActivePods: []workloadobserver.Pod{runPod},
		Continuous: true,
	}
	return raw
}

func idleRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	config := loadRunConfig(t)
	config.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode:   profilingrun.IdleModelLoadedWorkloadMode,
		InputGenerator: profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func validIdleRaw(t *testing.T, config profilingrun.RunConfig) IdleRawMeasurement {
	t.Helper()
	activeShape := validRaw(t, config)
	started := activeShape.Metrics.StartedAt
	return IdleRawMeasurement{
		SchemaVersion: IdleRawMeasurementSchemaVersion,
		RunConfig:     config,
		PodUID:        activeShape.PodUID,
		Runtime: profilingrun.IdleRuntimeResult{
			SchemaVersion: profilingrun.IdleRuntimeResultSchemaVersion, Status: "Succeeded",
			PodUID: activeShape.PodUID, NodeName: config.TargetNode.Name,
			Model:        config.State.Model,
			Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Backend: config.State.Backend, Provider: config.State.Backend,
			AvailableProviders:        []string{config.State.Backend},
			ModelLoadCompletedAt:      started.Add(-61 * time.Second).UTC().Format(time.RFC3339Nano),
			SettlingStartedAt:         started.Add(-60 * time.Second).UTC().Format(time.RFC3339Nano),
			SettlingCompletedAt:       started.UTC().Format(time.RFC3339Nano),
			MeasurementStartedAt:      started.UTC().Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).UTC().Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true,
			CPUContract: profilingrun.RuntimeCPU{
				CgroupCPUMax: "400000 100000", ORTIntraOpThreads: config.CPU.ORTIntraOpThreads,
				ORTInterOpThreads: config.CPU.ORTInterOpThreads,
				AffinityCPUCount:  config.CPU.ORTIntraOpThreads, CPUSetEffective: "0-3",
			},
			DerivedContract: profilingrun.DerivedContract{
				Backend: config.State.Backend, Provider: config.State.Backend,
				NodeSnapshot: config.TargetNode,
			},
		},
		Metrics:  activeShape.Metrics,
		Workload: activeShape.Workload,
	}
}

func validExecutionEvidence(
	t *testing.T,
	config profilingrun.RunConfig,
	raw RawMeasurement,
) ExecutionEvidence {
	t.Helper()
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return validExecutionEvidenceBytes(t, config, raw.PodUID, rawBytes)
}

func validIdleExecutionEvidence(
	t *testing.T,
	config profilingrun.RunConfig,
	raw IdleRawMeasurement,
) ExecutionEvidence {
	t.Helper()
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return validExecutionEvidenceBytes(t, config, raw.PodUID, rawBytes)
}

func validExecutionEvidenceBytes(
	t *testing.T,
	config profilingrun.RunConfig,
	podUID string,
	rawBytes []byte,
) ExecutionEvidence {
	t.Helper()
	location := time.FixedZone("KST", 9*60*60)
	completed := time.Date(2026, time.July, 16, 12, 1, 0, 0, location)
	container := func(name, image string) ContainerTerminal {
		return ContainerTerminal{
			Name:           name,
			RequestedImage: image,
			ImageID:        "containerd://" + name + "-image-id",
			Reason:         "Completed",
			StartedAt:      completed.Add(-time.Minute),
			FinishedAt:     completed.Add(-time.Second),
		}
	}
	return ExecutionEvidence{
		SchemaVersion: ExecutionEvidenceSchemaVersion,
		RunConfig:     config,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store: "chill-evidence",
			Key:   "runs/run-uid/raw.json",
			Size:  int64(len(rawBytes)),
		},
		Terminal: TerminalFacts{
			Job: JobTerminalFacts{
				Namespace:      "chill-system",
				Name:           "profiling-run",
				UID:            "job-uid",
				Succeeded:      1,
				CompletionTime: completed,
			},
			Pod: PodTerminalFacts{
				Namespace:          "chill-system",
				Name:               "profiling-run-pod",
				UID:                podUID,
				ControllerOwnerUID: "job-uid",
				NodeName:           config.TargetNode.Name,
				Phase:              "Succeeded",
			},
			Node: NodeTerminalFacts{Name: config.TargetNode.Name, UID: config.TargetNode.UID},
			Containers: []ContainerTerminal{
				container("metric-observer", config.Images.MetricObserver),
				container("workload-observer", config.Images.WorkloadObserver),
				container("runtime", config.Images.Runtime),
				container("artifact", config.Images.Artifact),
			},
			NodeWorkload: NodeWorkload{
				ObservedAt: completed.Add(time.Second),
				Pods: []WorkloadPod{
					{Namespace: "monitoring", Name: "edge-metric", UID: "edge-metric-uid", Phase: "Running", Images: []string{"image-b", "image-a"}},
					{Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID, Phase: "Succeeded", Images: []string{config.Images.Runtime}},
				},
			},
		},
		FinalizedAt: completed.Add(2 * time.Second),
	}
}

func validActuatedExecutionEvidence(
	t *testing.T,
) (profilingrun.RunConfig, RawMeasurement, ExecutionEvidence) {
	t.Helper()
	config := loadRunConfig(t)
	config.State.PowerMode = "MAXN"
	config.Actuation = &profilingrun.ActuationContract{
		PowerMode: "MAXN",
		Actuator: profilingrun.ActuatorTarget{
			Kind:           profilingrun.ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 0},
		},
		PolicyName: "jetson-mode-policy",
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	raw := validRaw(t, config)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	execution := validExecutionEvidenceBytes(t, config, raw.PodUID, rawBytes)
	execution.SchemaVersion = ExecutionEvidenceSchemaVersion
	completed := execution.Terminal.Job.CompletionTime
	execution.FinalizedAt = completed.Add(4 * time.Second)
	target := func(id int32) profilingrun.ActuatorTarget {
		return profilingrun.ActuatorTarget{
			Kind:           profilingrun.ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: id},
		}
	}
	execution.Actuation = &ActuationFacts{
		Run: NamespacedObjectFacts{
			Namespace: execution.Terminal.Job.Namespace, Name: "run", UID: "run-uid",
		},
		Handoff: NamespacedObjectFacts{
			Namespace: execution.Terminal.Job.Namespace, Name: "run-actuation", UID: "actuation-uid",
		},
		Target: ActuationObservation{
			PowerMode: config.Actuation.PowerMode, Actuator: target(0), ObservedAt: completed.Add(-90 * time.Second),
		},
		ReadyAt: completed.Add(-70 * time.Second),
	}
	return config, raw, execution
}

func cloneActuationFactsForTest(input *ActuationFacts) *ActuationFacts {
	if input == nil {
		return nil
	}
	copy := *input
	cloneTarget := func(target profilingrun.ActuatorTarget) profilingrun.ActuatorTarget {
		cloned := target
		if target.JetsonNVPModel != nil {
			value := *target.JetsonNVPModel
			cloned.JetsonNVPModel = &value
		}
		if target.NvidiaLockedGPUClock != nil {
			value := *target.NvidiaLockedGPUClock
			if value.Mode != nil {
				mode := *value.Mode
				value.Mode = &mode
			}
			cloned.NvidiaLockedGPUClock = &value
		}
		return cloned
	}
	copy.Target.Actuator = cloneTarget(copy.Target.Actuator)
	return &copy
}
