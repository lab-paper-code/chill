package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"

	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

type deriverEvidenceLoader struct {
	byRun map[string]profilerevidence.VerifiedIdleEvidence
}

func (l deriverEvidenceLoader) LoadIdle(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
	_ evidencestore.ObjectIdentity,
) (profilerevidence.VerifiedIdleEvidence, error) {
	verified, found := l.byRun[run.Name]
	if !found {
		return profilerevidence.VerifiedIdleEvidence{}, fmt.Errorf("unexpected Run %q", run.Name)
	}
	return verified, nil
}

func TestDeriverLoadsEvaluatesAndAggregatesAcceptedIdleEvidence(t *testing.T) {
	config := deriverIdleRunConfig(t)
	runs := make([]edgev1alpha1.ProfilingRun, 3)
	evidence := deriverEvidenceLoader{byRun: map[string]profilerevidence.VerifiedIdleEvidence{}}
	for index := range runs {
		name := fmt.Sprintf("idle-%d", index)
		runs[index] = edgev1alpha1.ProfilingRun{
			ObjectMeta: edgev1alpha1.ModelLoadedIdleBaseline{}.ObjectMeta,
			Status: edgev1alpha1.ProfilingRunStatus{

				Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
					Store: "evidence", Key: name + "/final.json",
					SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, Size: 10,
				},
			},
		}
		runs[index].Name = name
		evidence.byRun[name] = deriverIdleEvidence(t, config, name+"-pod", float64(10+index))
	}
	deriver, err := NewDeriver(evidence)
	if err != nil {
		t.Fatal(err)
	}
	point, err := pointPolicy(testBaselineParent().Spec.PointPolicy)
	if err != nil {
		t.Fatal(err)
	}
	baseline, references, err := deriver.Derive(context.Background(), runs, point)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.ModelLoadedIdleMeanWallWatts != 11 || len(baseline.Trials) != 3 || len(references) != 3 ||
		references["idle-2-pod"].Key != "idle-2/final.json" ||
		references["idle-2-pod"].SchemaVersion != profilerevidence.ExecutionEvidenceSchemaVersion {
		t.Fatalf("derived aggregate drifted: baseline=%#v refs=%#v", baseline, references)
	}
}

func deriverIdleRunConfig(t *testing.T) profilingrun.RunConfig {
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
	config.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050.onnx"}
	config.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	config.Workload = profilingrun.CurrentWorkloadContract()
	config.Measurement.Batch = 1
	config.Measurement.WarmupIterations = 0
	config.Measurement.SettlingDurationSeconds = 60
	config.Measurement.DurationSeconds = 30
	config.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	config.Measurement.Dataset = nil
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func deriverIdleEvidence(
	t *testing.T,
	config profilingrun.RunConfig,
	podUID string,
	watts float64,
) profilerevidence.VerifiedIdleEvidence {
	t.Helper()
	started := time.Date(2026, 8, 21, 1, 2, 3, 0, time.UTC)
	samples := make([]metricobserver.Sample, 30)
	for index := range samples {
		samples[index] = metricobserver.Sample{
			ObservedAt: started.Add(500*time.Millisecond + time.Duration(index)*time.Second), Watts: watts,
			Thermal: &metricobserver.ThermalReading{
				ObservationAvailable: deriverBool(true), Active: deriverBool(false),
			},
		}
	}
	workloadPod := workloadobserver.Pod{
		Namespace: "default", Name: "profiling-run-pod", UID: podUID,
		Phase: "Running", Images: []string{config.Images.Runtime},
	}
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
			Inputs:                    config.State.Preprocessor.Inputs,
			ModelLoadCompletedAt:      started.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt:         started.Add(-60 * time.Second).Format(time.RFC3339Nano),
			SettlingCompletedAt:       started.Format(time.RFC3339Nano),
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true,
			CPUContract: deriverRuntimeCPU(config),
			DerivedContract: profilingrun.DerivedContract{
				Backend: config.State.Backend, Provider: config.State.Backend, NodeSnapshot: config.TargetNode,
			},
		},
		Metrics: metricobserver.Result{
			Source: metricobserver.SourceIdentity{
				Kind: config.Metrics.SourceKind, NodeName: config.Metrics.NodeName,
				Endpoint: config.Metrics.Endpoint, Metric: config.Metrics.PowerMetric,
				MetricLabels: config.Metrics.MetricLabels, ThermalEndpoint: config.Metrics.ThermalEndpoint,
				ThermalMetrics: &metricobserver.ThermalMetricIdentity{
					ObservationAvailable: "device_thermal_throttle_observation_available",
					Active:               "device_thermal_throttle_active", CPUCoreThrottleEvents: "device_thermal_cpu_core_throttle_event_count",
					CPUPackageThrottleEvents: "device_thermal_cpu_package_throttle_event_count",
					Latched:                  "device_thermal_throttle_latched", MaximumTemperatureCelsius: "device_thermal_max_celsius",
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
			Target:        workloadobserver.TargetIdentity{NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID},
			Run: workloadobserver.RunIdentity{
				Namespace: "default", JobUID: "job-" + podUID, PodName: "profiling-run-pod", PodUID: podUID,
			},
			CoverageStartedAt: started.Add(-time.Second), CoverageEndedAt: started.Add(31 * time.Second),
			InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
			InitialActivePods: []workloadobserver.Pod{workloadPod}, FinalActivePods: []workloadobserver.Pod{workloadPod},
			Continuous: true,
		},
	}
	applyFirstfloorIdleRuntime(config, &raw.Runtime)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	execution := deriverExecution(t, config, podUID, rawBytes)
	executionBytes, err := execution.CanonicalIdleJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	reference := evidencestore.ObjectIdentity{
		Store: "evidence", Key: "execution-" + podUID + ".json", Size: int64(len(executionBytes)),
	}
	loader, err := profilerevidence.NewLoader(deriverStore{
		execution.RawMeasurement.Location(): rawBytes, reference.Location(): executionBytes,
	}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := loader.LoadIdle(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func deriverExecution(
	t *testing.T,
	config profilingrun.RunConfig,
	podUID string,
	rawBytes []byte,
) profilerevidence.ExecutionEvidence {
	t.Helper()
	completed := time.Date(2026, 8, 21, 1, 3, 3, 0, time.UTC)
	container := func(name, image string) profilerevidence.ContainerTerminal {
		return profilerevidence.ContainerTerminal{
			Name: name, RequestedImage: image, ImageID: "containerd://" + name,
			Reason: "Completed", StartedAt: completed.Add(-time.Minute), FinishedAt: completed.Add(-time.Second),
		}
	}
	return profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, RunConfig: config,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store: "evidence", Key: "raw-" + podUID + ".json", Size: int64(len(rawBytes)),
		},
		Terminal: profilerevidence.TerminalFacts{
			Job: profilerevidence.JobTerminalFacts{
				Namespace: "default", Name: "job-" + podUID, UID: "job-" + podUID, Succeeded: 1, CompletionTime: completed,
			},
			Pod: profilerevidence.PodTerminalFacts{
				Namespace: "default", Name: "profiling-run-pod", UID: podUID,
				ControllerOwnerUID: "job-" + podUID, NodeName: config.TargetNode.Name, Phase: "Succeeded",
			},
			Node: profilerevidence.NodeTerminalFacts{Name: config.TargetNode.Name, UID: config.TargetNode.UID},
			Containers: []profilerevidence.ContainerTerminal{
				container("metric-observer", config.Images.MetricObserver), container("workload-observer", config.Images.WorkloadObserver),
				container("runtime", config.Images.Runtime), container("artifact", config.Images.Artifact),
			},
			NodeWorkload: profilerevidence.NodeWorkload{
				ObservedAt: completed.Add(time.Second), Pods: []profilerevidence.WorkloadPod{{
					Namespace: "default", Name: "profiling-run-pod", UID: podUID,
					Phase: "Succeeded", Images: []string{config.Images.Runtime},
				}},
			},
		},
		FinalizedAt: completed.Add(2 * time.Second),
	}
}

func deriverRuntimeCPU(config profilingrun.RunConfig) profilingrun.RuntimeCPU {
	return profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: config.CPU.ORTIntraOpThreads,
		ORTInterOpThreads: config.CPU.ORTInterOpThreads, AffinityCPUCount: config.CPU.ORTIntraOpThreads,
		CPUSetEffective: "0-3", CgroupCPUStatDelta: map[string]int{"user_usec": 1, "system_usec": 2},
	}
}

type deriverStore map[evidencestore.ObjectLocation][]byte

func (s deriverStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	payload, found := s[location]
	if !found {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func deriverBool(value bool) *bool { return &value }
