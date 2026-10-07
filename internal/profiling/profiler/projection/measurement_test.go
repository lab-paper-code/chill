package projection

import (
	"bytes"
	"context"
	"encoding/json"

	"io"
	"os"

	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"

	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestProjectionRequiresVerifiedEvidence(t *testing.T) {
	t.Parallel()
	if _, err := Trial(profilerevidence.VerifiedEvidence{}); err == nil {
		t.Fatal("empty active evidence was accepted")
	}
	if _, err := IdleTrial(profilerevidence.VerifiedIdleEvidence{}); err == nil {
		t.Fatal("empty idle evidence was accepted")
	}
}

func TestTrialProjectsVerifiedEvidence(t *testing.T) {
	evidence := acceptedActiveFixture(t)
	trial, err := Trial(evidence)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trial.Batch < 1 || trial.TrialID == "" {
		t.Fatalf("accepted evaluation returned no usable trial: %+v", trial)
	}
}

func TestProjectTrialPreservesFactsAndDoesNotInferWindowFactsFromTerminalSnapshot(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	config := projectionRunConfig()
	execution := profilerevidence.ExecutionEvidence{
		RunConfig: config,
		Terminal: profilerevidence.TerminalFacts{
			Pod:  profilerevidence.PodTerminalFacts{UID: "run-pod"},
			Node: profilerevidence.NodeTerminalFacts{UID: "node-uid"},
			NodeWorkload: profilerevidence.NodeWorkload{Pods: []profilerevidence.WorkloadPod{
				{Namespace: "chill-system", Name: "run", UID: "run-pod", Phase: "Succeeded"},
				{Namespace: "monitoring", Name: "edge-metric", UID: "metrics", Phase: "Running"},
				{Namespace: "default", Name: "finished", UID: "finished", Phase: "Succeeded"},
			}},
		},
	}
	raw := profilerevidence.RawMeasurement{
		RunConfig: config,
		Runtime: profilingrun.RuntimeResult{
			Inputs:               modelcompat.CloneInputTensors(config.Delivery.ArtifactCapability.Tensors),
			MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt:   started.Add(30 * time.Second).Format(time.RFC3339Nano),
			InferenceCount:       2,
			RepetitionResults: []profilingrun.Repetition{{
				LatencyMilliseconds: []float64{1, 2},
			}},
		},
		Metrics: metricobserver.Result{
			Samples:  []metricobserver.Sample{{ObservedAt: started, Watts: 4}},
			Failures: []metricobserver.Failure{{Reason: metricobserver.FailureReasonTimeout}},
			Summary:  metricobserver.Summary{SourceTimestampAbsent: true},
		},
	}

	trial, err := projectTrial(execution, raw)
	if err != nil {
		t.Fatal(err)
	}
	if trial.CompletedCalls != 2 || trial.CompletedItems != 4 || len(trial.LatencyMilliseconds) != 2 ||
		len(trial.Samples) != 1 || trial.Failures != 1 || !trial.SourceTimestampAbsent || trial.WorkloadWindowKnown {
		t.Fatalf("projected trial = %#v", trial)
	}
	if trial.StateIdentity == "" || trial.EnvironmentIdentity == "" ||
		trial.MeasurementProtocolIdentity == "" {
		t.Fatalf("projected identities = %#v", trial)
	}
	if len(trial.ConcurrentCHILLWorkloads) != 0 || len(trial.ExternalWorkloadChanges) != 0 {
		t.Fatalf("terminal snapshot was treated as measurement-window evidence: %#v", trial)
	}
}

func TestProjectIdleObservationPreservesRawProvenanceAndRuntimeWindow(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	config := projectionRunConfig()
	config.Measurement.SettlingDurationSeconds = 60
	execution := profilerevidence.ExecutionEvidence{
		RunConfig: config,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store: "chill-evidence", Key: "runs/idle/raw.json",
			Size: 1024,
		},
		Terminal: profilerevidence.TerminalFacts{
			Pod:  profilerevidence.PodTerminalFacts{UID: "idle-pod"},
			Node: profilerevidence.NodeTerminalFacts{UID: "node-uid"},
		},
	}
	raw := profilerevidence.IdleRawMeasurement{
		RunConfig: config,
		Runtime: profilingrun.IdleRuntimeResult{
			Inputs:               modelcompat.CloneInputTensors(config.Delivery.ArtifactCapability.Tensors),
			ModelLoadCompletedAt: started.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt:    started.Add(-60 * time.Second).Format(time.RFC3339Nano),
			SettlingCompletedAt:  started.Format(time.RFC3339Nano),
			MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt:   started.Add(30 * time.Second).Format(time.RFC3339Nano),
			ModelResidentAtEnd:   true,
		},
		Metrics: metricobserver.Result{Samples: []metricobserver.Sample{{ObservedAt: started, Watts: 4}}},
	}

	observation, err := projectIdleObservation(execution, raw)
	if err != nil {
		t.Fatal(err)
	}
	if observation.TrialID != "idle-pod" || observation.RawEvidence.Store != "chill-evidence" ||
		observation.RawEvidence.Key != "runs/idle/raw.json" ||
		!observation.ModelResidentAtEnd || observation.InferenceCountAfterModelLoad != 0 ||
		!observation.WindowStart.Equal(started) || !observation.WindowEnd.Equal(started.Add(30*time.Second)) {
		t.Fatalf("projected idle observation = %#v", observation)
	}
}

func projectionBool(value bool) *bool { return &value }

func projectionRunConfig() profilingrun.RunConfig {
	return profilingrun.RunConfig{
		State: profilingrun.ExecutionState{
			DeviceClass: "lattepanda", Model: "mobilenet",
			RuntimeFamily: "onnxruntime", Backend: "CPUExecutionProvider", PowerMode: "fixed",
		},
		Images: profilingrun.Images{Runtime: "runtime", Artifact: "artifact", MetricObserver: "observer"},
		Delivery: profilingrun.CandidateRuntimeDelivery{
			ArtifactCapability: &profilingrun.CandidateArtifactCapability{
				Tensors: []modelcompat.InputTensor{
					{Name: "input", Datatype: "FP32", SampleShape: []int32{3, 224, 224}},
				},
			},
		},
		TargetNode: profilingrun.NodeSnapshot{Name: "lattepanda", UID: "node-uid"},
		CPU:        profilingrun.CPUContract{Policy: "allocatable", PolicyVersion: "v1", Limit: "4"},
		Measurement: profilingrun.MeasurementContract{
			Batch: 2, WarmupIterations: 20, DurationSeconds: 30,
			WorkloadMode: "saturated", InputGenerator: "seeded", OutputValidation: "shape",
		},
		Metrics: profilingrun.MetricContract{
			SourceKind: "edge-metrics", NodeName: "lattepanda",
			Endpoint: "http://central-meter/prometheus?hostname=lattepanda", PowerMetric: "watts",
			MetricLabels:    map[string]string{"hostname": "lattepanda", "device_type": "test_device"},
			ThermalEndpoint: "http://thermal/metrics",
			ThermalMetrics: profilingrun.ThermalMetricContract{
				ObservationAvailable: "thermal_observation_available", Active: "thermal_active",
				CPUCoreThrottleEvents: "thermal_cpu_core_events", CPUPackageThrottleEvents: "thermal_cpu_package_events",
				Latched: "thermal_latched", MaximumTemperatureCelsius: "thermal_max_celsius",
			},
			IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
		},
	}
}

type activeFixtureStore map[evidencestore.ObjectLocation][]byte

func (s activeFixtureStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	payload, ok := s[location]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func acceptedActiveFixture(t *testing.T) profilerevidence.VerifiedEvidence {
	t.Helper()
	intentBytes, err := os.ReadFile("../evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(intentBytes, &config); err != nil {
		t.Fatal(err)
	}
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	config.Workload = profilingrun.CurrentWorkloadContract()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	podUID := "run-pod"
	targetDuration := float64(config.Measurement.DurationSeconds)
	samples := make([]metricobserver.Sample, 30)
	for index := range samples {
		samples[index] = metricobserver.Sample{
			ObservedAt: started.Add(500*time.Millisecond + time.Duration(index)*time.Second),
			Watts:      5.2,
			Thermal: &metricobserver.ThermalReading{
				ObservationAvailable: projectionBool(true),
				Active:               projectionBool(false),
			},
		}
	}
	workload := workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: config.TargetNode.Name,
			NodeUID:  config.TargetNode.UID,
		},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system",
			JobUID:    "job-uid",
			PodName:   "profiling-run-pod",
			PodUID:    podUID,
		},
		CoverageStartedAt:      started.Add(-time.Second),
		CoverageEndedAt:        started.Add(31 * time.Second),
		InitialResourceVersion: "10",
		LastResourceVersion:    "10",
		EventLimit:             10,
		InitialActivePods: []workloadobserver.Pod{{
			Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
			Phase: "Running", Images: []string{config.Images.Runtime},
		}},
		FinalActivePods: []workloadobserver.Pod{{
			Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
			Phase: "Running", Images: []string{config.Images.Runtime},
		}},
		Continuous: true,
	}
	raw := profilerevidence.RawMeasurement{
		SchemaVersion: profilerevidence.RawMeasurementSchemaVersion,
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
			Inputs:                    modelcompat.CloneInputTensors(config.Delivery.ArtifactCapability.Tensors),
			BatchSize:                 config.Measurement.Batch,
			WarmupIterations:          config.Measurement.WarmupIterations,
			WarmupCompletedAt:         started.Add(-time.Second).Format(time.RFC3339Nano),
			InferenceCount:            2,
			MeasurementMode:           "duration",
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30,
			TargetDurationSeconds:     &targetDuration,
			OutputValidation: profilingrun.OutputValidation{
				Method: config.Measurement.OutputValidation, Passed: true,
				OutputShapes: [][]int{{config.Measurement.Batch, 1000}},
			},
			CPUContract: profilingrun.RuntimeCPU{
				CgroupCPUMax: "400000 100000", ORTIntraOpThreads: config.CPU.ORTIntraOpThreads,
				ORTInterOpThreads: config.CPU.ORTInterOpThreads,
				AffinityCPUCount:  config.CPU.ORTIntraOpThreads,
				CPUSetEffective:   "0-3",
				CgroupCPUStatDelta: map[string]int{
					"user_usec": 1, "system_usec": 2,
				},
			},
			DerivedContract: profilingrun.DerivedContract{
				Backend: config.State.Backend, Provider: config.State.Backend,
				NodeSnapshot: config.TargetNode,
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
					ObservationAvailable:      config.Metrics.ThermalMetrics.ObservationAvailable,
					Active:                    config.Metrics.ThermalMetrics.Active,
					CPUCoreThrottleEvents:     config.Metrics.ThermalMetrics.CPUCoreThrottleEvents,
					CPUPackageThrottleEvents:  config.Metrics.ThermalMetrics.CPUPackageThrottleEvents,
					Latched:                   config.Metrics.ThermalMetrics.Latched,
					MaximumTemperatureCelsius: config.Metrics.ThermalMetrics.MaximumTemperatureCelsius,
				},
			},
			StartedAt: started, EndedAt: started.Add(31 * time.Second), Samples: samples,
			Summary: metricobserver.Summary{
				Attempts: 30, SuccessfulSamples: 30, ObservationDurationSeconds: 31,
				MaximumSampleGapSeconds: 1, SourceTimestampAbsent: true,
			},
		},
		Workload: &workload,
	}
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	rawLocation := evidencestore.ObjectLocation{Store: "evidence", Key: "raw.json"}
	rawIdentity := evidencestore.ObjectIdentity{
		Store: rawLocation.Store, Key: rawLocation.Key, Size: int64(len(rawBytes)),
	}
	completed := started.Add(time.Minute)
	container := func(name, image string) profilerevidence.ContainerTerminal {
		return profilerevidence.ContainerTerminal{
			Name: name, RequestedImage: image, ImageID: "containerd://" + name,
			Reason: "Completed", StartedAt: completed.Add(-time.Minute),
			FinishedAt: completed.Add(-time.Second),
		}
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		RunConfig:     config, RawMeasurement: rawIdentity,
		Terminal: profilerevidence.TerminalFacts{
			Job: profilerevidence.JobTerminalFacts{
				Namespace: "chill-system", Name: "profiling-run", UID: "job-uid",
				Succeeded: 1, CompletionTime: completed,
			},
			Pod: profilerevidence.PodTerminalFacts{
				Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
				ControllerOwnerUID: "job-uid", NodeName: config.TargetNode.Name, Phase: "Succeeded",
			},
			Node: profilerevidence.NodeTerminalFacts{Name: config.TargetNode.Name, UID: config.TargetNode.UID},
			Containers: []profilerevidence.ContainerTerminal{
				container("metric-observer", config.Images.MetricObserver),
				container("workload-observer", config.Images.WorkloadObserver),
				container("runtime", config.Images.Runtime),
				container("artifact", config.Images.Artifact),
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
	executionBytes, err := execution.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	executionLocation := evidencestore.ObjectLocation{Store: "evidence", Key: "execution.json"}
	executionIdentity := evidencestore.ObjectIdentity{
		Store: executionLocation.Store, Key: executionLocation.Key, Size: int64(len(executionBytes)),
	}
	loader, err := profilerevidence.NewLoader(activeFixtureStore{
		rawLocation: rawBytes, executionLocation: executionBytes,
	}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := loader.Load(context.Background(), executionIdentity)
	if err != nil {
		t.Fatal(err)
	}

	return verified
}
