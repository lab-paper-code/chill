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
	profileradmission "github.com/lab-paper-code/chill/internal/profiling/profiler/admission"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

type deriverIntentLoader struct {
	frozen profilercontroller.FrozenRun
}

func (l deriverIntentLoader) Load(
	context.Context,
	*edgev1alpha1.ProfilingRun,
) (profilercontroller.FrozenRun, error) {
	return l.frozen, nil
}

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
	intent := deriverIdleIntent(t)
	policy := deriverIdlePolicy(t)
	runs := make([]edgev1alpha1.ProfilingRun, 3)
	evidence := deriverEvidenceLoader{byRun: map[string]profilerevidence.VerifiedIdleEvidence{}}
	for index := range runs {
		name := fmt.Sprintf("idle-%d", index)
		runs[index] = edgev1alpha1.ProfilingRun{
			ObjectMeta: edgev1alpha1.ModelLoadedIdleBaseline{}.ObjectMeta,
			Status: edgev1alpha1.ProfilingRunStatus{
				Admission: &edgev1alpha1.ProfilingRunAdmissionStatus{Verdict: "Accepted"},
				Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
					Store: "evidence", Key: name + "/final.json",
					SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, Size: 10,
				},
			},
		}
		runs[index].Name = name
		evidence.byRun[name] = deriverIdleEvidence(t, intent, name+"-pod", float64(10+index))
	}
	deriver, err := NewDeriver(deriverIntentLoader{frozen: profilercontroller.FrozenRun{
		AdmissionPolicy: policy,
	}}, evidence)
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

func deriverIdleIntent(t *testing.T) profilingrun.Intent {
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
	intent.Measurement.Batch = 1
	intent.Measurement.WarmupIterations = 0
	intent.Measurement.SettlingDurationSeconds = 60
	intent.Measurement.DurationSeconds = 30
	intent.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	intent.Measurement.InputGenerator = "none"
	intent.Measurement.OutputValidation = "none"
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func deriverIdlePolicy(t *testing.T) profileradmission.FrozenPolicy {
	t.Helper()
	payload := []byte(`
schemaVersion: chill.dacs.io/profiler-admission-policies.v1alpha1
policies:
  - name: idle-admission-v1
    workloadMode: idle-model-loaded
    minimumSamples: 20
    maximumGapSeconds: 1.5
    maximumBoundaryDistanceSeconds: 1
    minimumCoverage: 0.95
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
`)
	policy, err := profileradmission.FreezeConfiguredPolicy(payload, "idle-admission-v1", profileradmission.PolicySource{
		Kind: profileradmission.ConfigMapPolicySourceKind, Namespace: "default",
		Name: "policies", UID: "policy-uid", ResourceVersion: "1", Key: "policies.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func deriverIdleEvidence(
	t *testing.T,
	intent profilingrun.Intent,
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
		Phase: "Running", Images: []string{intent.Images.Runtime},
	}
	raw := profilerevidence.IdleRawMeasurement{
		SchemaVersion: profilerevidence.IdleRawMeasurementSchemaVersion, PodUID: podUID,
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
			CPUContract: deriverRuntimeCPU(intent),
			DerivedContract: profilingrun.DerivedContract{
				Backend: intent.State.Backend, Provider: intent.State.Backend, NodeSnapshot: intent.TargetNode,
			},
		},
		Metrics: metricobserver.Result{
			Source: metricobserver.SourceIdentity{
				Kind: intent.Metrics.SourceKind, NodeName: intent.Metrics.NodeName,
				Endpoint: intent.Metrics.Endpoint, Metric: intent.Metrics.PowerMetric,
				MetricLabels: intent.Metrics.MetricLabels, ThermalEndpoint: intent.Metrics.ThermalEndpoint,
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
			Target:        workloadobserver.TargetIdentity{NodeName: intent.TargetNode.Name, NodeUID: intent.TargetNode.UID},
			Run: workloadobserver.RunIdentity{
				Namespace: "default", JobUID: "job-" + podUID, PodName: "profiling-run-pod", PodUID: podUID,
			},
			CoverageStartedAt: started.Add(-time.Second), CoverageEndedAt: started.Add(31 * time.Second),
			InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
			InitialActivePods: []workloadobserver.Pod{workloadPod}, FinalActivePods: []workloadobserver.Pod{workloadPod},
			Continuous: true,
		},
	}
	rawBytes, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	execution := deriverExecution(t, intent, podUID, rawBytes)
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
	completed := time.Date(2026, 8, 21, 1, 3, 3, 0, time.UTC)
	container := func(name, image string) profilerevidence.ContainerTerminal {
		return profilerevidence.ContainerTerminal{
			Name: name, RequestedImage: image, ImageID: "containerd://" + name,
			Reason: "Completed", StartedAt: completed.Add(-time.Minute), FinishedAt: completed.Add(-time.Second),
		}
	}
	return profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, Intent: intent, CandidateReport: candidate,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store: "evidence", Key: "raw-" + podUID + ".json", Size: int64(len(rawBytes)),
		},
		Terminal: profilerevidence.TerminalFacts{
			Job: profilerevidence.JobTerminalFacts{
				Namespace: "default", Name: "job-" + podUID, UID: "job-" + podUID, Succeeded: 1, CompletionTime: completed,
			},
			Pod: profilerevidence.PodTerminalFacts{
				Namespace: "default", Name: "profiling-run-pod", UID: podUID,
				ControllerOwnerUID: "job-" + podUID, NodeName: intent.TargetNode.Name, Phase: "Succeeded",
			},
			Node: profilerevidence.NodeTerminalFacts{Name: intent.TargetNode.Name, UID: intent.TargetNode.UID},
			Containers: []profilerevidence.ContainerTerminal{
				container("metric-observer", intent.Images.MetricObserver), container("workload-observer", intent.Images.WorkloadObserver),
				container("runtime", intent.Images.Runtime), container("artifact", intent.Images.Artifact),
			},
			NodeWorkload: profilerevidence.NodeWorkload{
				ObservedAt: completed.Add(time.Second), Pods: []profilerevidence.WorkloadPod{{
					Namespace: "default", Name: "profiling-run-pod", UID: podUID,
					Phase: "Succeeded", Images: []string{intent.Images.Runtime},
				}},
			},
		},
		FinalizedAt: completed.Add(2 * time.Second),
	}
}

func deriverRuntimeCPU(intent profilingrun.Intent) profilingrun.RuntimeCPU {
	return profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: intent.CPU.ORTIntraOpThreads,
		ORTInterOpThreads: intent.CPU.ORTInterOpThreads, AffinityCPUCount: intent.CPU.ORTIntraOpThreads,
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
