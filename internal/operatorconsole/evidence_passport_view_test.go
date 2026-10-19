package operatorconsole

import (
	"reflect"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildEvidencePassportProjectsBoundedMeasuredFacts(t *testing.T) {
	t.Parallel()
	const rawObjectKey = "raw.json"
	run, execution := passportFixture()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	execution.FinalizedAt = started.Add(time.Minute)
	execution.RawMeasurement.Store = "chill-evidence"
	execution.RawMeasurement.Key = rawObjectKey
	execution.RawMeasurement.Size = 512
	execution.RunConfig = profilingrun.RunConfig{Workload: profilingrun.CurrentWorkloadContract()}
	workload := passportWorkloadResult(t, started)
	measurement := profilerevidence.RawMeasurement{
		Runtime: profilingrun.RuntimeResult{
			Runtime: "onnxruntime", RuntimeVersion: "1.16.0", Provider: "CPUExecutionProvider",
			Architecture: "x86_64", BatchSize: 4, WarmupIterations: 20,
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30.001, InferenceCount: 3,
			ThroughputItemsPerSecond: 400,
			RepetitionResults: []profilingrun.Repetition{
				{LatencyMilliseconds: []float64{3, 1}, NativeSamples: []profilingrun.NativeServiceSample{
					{BatchID: 1, BatchSize: 4, RecordIDs: []string{"a", "b", "c", "d"}, Units: []int{1, 1, 1, 1}},
					{BatchID: 2, BatchSize: 4, RecordIDs: []string{"e", "f", "g", "h"}, Units: []int{1, 1, 1, 1}},
				}},
				{LatencyMilliseconds: []float64{2}, NativeSamples: []profilingrun.NativeServiceSample{
					{BatchID: 3, BatchSize: 4, RecordIDs: []string{"i", "j", "k", "l"}, Units: []int{1, 1, 1, 1}},
				}},
			},
		},
		Metrics: metricobserver.Result{
			Source: metricobserver.SourceIdentity{
				Kind: "edge-metrics", NodeName: "lattepanda", Metric: "shelly_watts", Endpoint: "http://internal",
				ThermalMetrics: &metricobserver.ThermalMetricIdentity{Active: "device_thermal_throttle_active"},
			},
			StartedAt: started, EndedAt: started.Add(31 * time.Second),
			Samples: []metricobserver.Sample{
				{Watts: 8, Thermal: testThermalReading(true, false, false, 7, 42)},
				{Watts: 10, Thermal: testThermalReading(true, true, true, 8, 44)},
				{Watts: 18, Thermal: testThermalReading(false, false, false, 8, 43)},
			},
			Summary: metricobserver.Summary{
				Attempts: 3, SuccessfulSamples: 3, ObservationDurationSeconds: 31,
				MeanRequestLatencySeconds: 0.03, P95RequestLatencySeconds: 0.04,
				MaximumSampleGapSeconds: 1.1, SourceTimestampAbsent: true,
			},
		},
		Workload: &workload,
	}

	got, err := BuildEvidencePassport(EvidencePassportSnapshot{
		ReadAt: started.Add(2 * time.Minute), Run: *run, Execution: execution, Measurement: measurement,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.APIVersion != EvidencePassportAPIVersion || got.Kind != "EvidencePassport" ||
		got.Run.Name != run.Name || got.Evidence.Raw.Key != rawObjectKey {
		t.Fatalf("passport identity = %#v", got)
	}
	runtime := got.Measurement.Runtime
	if runtime.CompletedCalls != 3 || runtime.CompletedItems != 12 || runtime.Latency.Count != 3 ||
		runtime.ThroughputItems != 2000 || runtime.Latency.Mean != 2 || runtime.Latency.P50 != 2 ||
		runtime.Latency.P95 != 3 || runtime.Latency.P99 != 3 {
		t.Fatalf("runtime facts = %#v", runtime)
	}
	if len(runtime.NativeSamples) != 3 || runtime.NativeSamples[0].BatchID != 1 ||
		runtime.NativeSamples[2].BatchID != 3 || runtime.NativeSamples[2].RecordIDs[3] != "l" {
		t.Fatalf("native samples = %#v", runtime.NativeSamples)
	}
	power := got.Measurement.Power
	if power.Watts == nil || power.Watts.Minimum != 8 || power.Watts.Mean != 12 || power.Watts.Maximum != 18 ||
		power.Source.Kind != "edge-metrics" || power.Source.Metric != "shelly_watts" || power.Samples != 3 ||
		!power.SourceTimestampAbsent {
		t.Fatalf("power facts = %#v", power)
	}
	wantThermal := EvidencePassportThermal{
		Metrics:                     &EvidencePassportThermalMetrics{Active: "device_thermal_throttle_active"},
		Samples:                     3,
		ObservationAvailableSamples: 2,
		ActiveSamples:               1,
		LatchedSamples:              1,
		CPUCoreThrottleEvents:       &EvidencePassportCounter{Samples: 3, First: 7, Last: 8},
		MaximumTemperatureCelsius:   &EvidencePassportRange{Samples: 3, Minimum: 42, Maximum: 44},
	}
	if !reflect.DeepEqual(got.Measurement.Thermal, wantThermal) {
		t.Fatalf("thermal facts = %#v, want %#v", got.Measurement.Thermal, wantThermal)
	}
	assertPassportWorkload(t, got.Measurement.Workload)
}

func assertPassportWorkload(t *testing.T, workload *EvidencePassportWorkload) {
	t.Helper()
	if workload == nil || !workload.Continuous || workload.InitialActivePods != 3 ||
		len(workload.CoResidents) != 2 || workload.CoResidents[0].Name != "fastdds" ||
		workload.CoResidents[0].CHILLMeasurement || workload.CoResidents[0].Changed || workload.CoResidents[0].CHILLPlaced ||
		workload.CoResidents[1].CHILLMeasurement || workload.CoResidents[1].Changed || !workload.CoResidents[1].CHILLPlaced {
		t.Fatalf("workload facts = %#v", workload)
	}
}

func passportWorkloadResult(
	t *testing.T,
	started time.Time,
) workloadobserver.Result {
	t.Helper()
	run := workloadobserver.Pod{
		Namespace: "chill-system", Name: "run", UID: "run-uid", Phase: "Running",
		Images: []string{"runtime@sha256:" + strings.Repeat("a", 64)},
	}
	unexpected := workloadobserver.Pod{
		Namespace: "fastdds-test", Name: "fastdds", UID: "fastdds-uid", Phase: "Running",
		Images: []string{"fastdds@sha256:" + strings.Repeat("b", 64)},
	}
	allowed := workloadobserver.Pod{
		Namespace: "monitoring", Name: "edge-metrics", UID: "metrics-uid", Phase: "Running",
		Owner:  &workloadobserver.OwnerIdentity{Kind: "DaemonSet", Name: "edge-metrics", UID: "owner-uid"},
		Images: []string{"metrics@sha256:" + strings.Repeat("c", 64)}, Labels: map[string]string{"app": "edge-metrics", profilingrun.MeasurementWorkloadLabelKey: profilingrun.SystemRole},
	}
	pods := []workloadobserver.Pod{run, unexpected, allowed}
	return workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target:        workloadobserver.TargetIdentity{NodeName: "lattepanda", NodeUID: "node-uid"},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system", JobUID: "job-uid", PodName: "run", PodUID: "run-uid",
		},
		CoverageStartedAt: started, CoverageEndedAt: started.Add(31 * time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "10", EventLimit: 10,
		InitialActivePods: pods, FinalActivePods: append([]workloadobserver.Pod(nil), pods...), Continuous: true,
	}
}

func testThermalReading(available, active, latched bool, events uint64, temperature float64) *metricobserver.ThermalReading {
	return &metricobserver.ThermalReading{
		ObservationAvailable: &available, Active: &active, Latched: &latched,
		CPUCoreThrottleEvents: &events, MaximumTemperatureCelsius: &temperature,
	}
}

func TestBuildEvidencePassportRepresentsNoSuccessfulPowerSamples(t *testing.T) {
	t.Parallel()
	run, execution := passportFixture()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	passport, err := BuildEvidencePassport(EvidencePassportSnapshot{
		ReadAt:    started,
		Run:       *run,
		Execution: execution,
		Measurement: profilerevidence.RawMeasurement{
			Runtime: profilingrun.RuntimeResult{
				BatchSize:            1,
				MeasurementStartedAt: started.Format(time.RFC3339Nano),
				MeasurementEndedAt:   started.Add(time.Second).Format(time.RFC3339Nano),
				InferenceCount:       1,
				RepetitionResults:    []profilingrun.Repetition{{LatencyMilliseconds: []float64{1}}},
			},
			Metrics: metricobserver.Result{Summary: metricobserver.Summary{Failures: 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if passport.Measurement.Power.Watts != nil || passport.Measurement.Power.Failures != 1 {
		t.Fatalf("power facts = %#v", passport.Measurement.Power)
	}
}

// passportFixture builds the minimum verified evidence this projection
// accepts. The adapter package keeps its own copy for its own reasons.
func passportFixture() (*edgev1alpha1.ProfilingRun, profilerevidence.ExecutionEvidence) {
	identity := evidencestore.ObjectIdentity{
		Store: "chill-evidence", Key: "profiling-runs/run/execution.json",
		Size: 1024,
	}
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: consoleTestNamespace, Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			ObservedGeneration: 1,
			Job:                &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			ObservedExecution: &edgev1alpha1.ProfilingRunObservedExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "node-uid"},
			},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key,
				Size: identity.Size, SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: consoleTestNamespace, Name: "job", UID: "job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: consoleTestNamespace, Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
	}
	return run, execution
}
