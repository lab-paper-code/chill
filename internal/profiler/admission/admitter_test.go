package admission

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

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestEvaluateRequiresVerifiedEvidence(t *testing.T) {
	t.Parallel()
	if _, err := Evaluate(profilerevidence.VerifiedEvidence{}, FrozenPolicy{}); err == nil {
		t.Fatal("empty active evidence was accepted")
	}
	if _, _, err := EvaluateIdle(profilerevidence.VerifiedIdleEvidence{}, FrozenPolicy{}); err == nil {
		t.Fatal("empty idle evidence was accepted")
	}
}

func TestEvaluateTrialReturnsTheTrialOnlyWhenAccepted(t *testing.T) {
	evidence, policy := acceptedActiveFixture(t)
	result, trial, err := EvaluateTrial(evidence, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != VerdictAccepted {
		t.Fatalf("verdict = %q, want Accepted", result.Verdict)
	}
	if trial == nil || trial.Batch < 1 || trial.TrialID == "" {
		t.Fatalf("accepted evaluation returned no usable trial: %+v", trial)
	}
}

func TestEvaluateTrialPreservesNonAcceptedAndErrorReturns(t *testing.T) {
	t.Run("non-Accepted", func(t *testing.T) {
		evidence, _ := acceptedActiveFixture(t)
		policy := activeFixturePolicy(t, "insufficient-active", 31)

		result, trial, err := EvaluateTrial(evidence, policy)
		if err != nil {
			t.Fatalf("EvaluateTrial() error = %v", err)
		}
		if result.Verdict != VerdictInsufficient || result.PolicyVersion == "" ||
			len(result.ReasonCodes) == 0 || trial != nil {
			t.Fatalf("EvaluateTrial() = (%+v, %+v, nil), want populated Insufficient result and nil trial",
				result, trial)
		}
	})

	t.Run("error", func(t *testing.T) {
		_, policy := acceptedActiveFixture(t)

		result, trial, err := EvaluateTrial(profilerevidence.VerifiedEvidence{}, policy)
		if err == nil || !reflect.DeepEqual(result, Result{}) || trial != nil {
			t.Fatalf("EvaluateTrial() = (%+v, %+v, %v), want zero result, nil trial, and error",
				result, trial, err)
		}
	})
}

func TestEvaluateCurveRunRetainsTheAdmissionThatAuthorizedTheTrial(t *testing.T) {
	evidence, policy := acceptedActiveFixture(t)
	compact, curveRun, err := EvaluateCurveRun(evidence, policy)
	if err != nil {
		t.Fatalf("EvaluateCurveRun() error = %v", err)
	}
	trialCompact, trial, err := EvaluateTrial(evidence, policy)
	if err != nil {
		t.Fatalf("EvaluateTrial() error = %v", err)
	}
	if curveRun == nil || trial == nil {
		t.Fatalf("accepted evaluation omitted projections: curveRun=%+v trial=%+v", curveRun, trial)
	}
	if !reflect.DeepEqual(compact, trialCompact) || !reflect.DeepEqual(curveRun.Trial, *trial) {
		t.Fatalf("evaluation projections drifted: curve=(%+v, %+v) trial=(%+v, %+v)",
			compact, curveRun.Trial, trialCompact, *trial)
	}
	admissionPolicy, err := policy.AdmissionPolicy()
	if err != nil {
		t.Fatal(err)
	}
	wantAdmission := profilederivation.Admit(curveRun.Trial, admissionPolicy)
	if wantAdmission.Verdict != profilederivation.VerdictAccepted ||
		!reflect.DeepEqual(curveRun.Admission, wantAdmission) {
		t.Fatalf("retained admission did not authorize the trial: got=%+v want=%+v",
			curveRun.Admission, wantAdmission)
	}
}

func TestProjectTrialPreservesFactsAndDoesNotInferWindowFactsFromTerminalSnapshot(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	intent := admissionIntent()
	execution := profilerevidence.ExecutionEvidence{
		Intent: intent,
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
		Runtime: profilingrun.RuntimeResult{
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
		len(trial.Samples) != 1 || trial.Failures != 1 || !trial.SourceTimestampAbsent ||
		!trial.SteadyWindowVerified || trial.ThermalStateKnown || trial.WorkloadWindowKnown {
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
	intent := admissionIntent()
	intent.Measurement.SettlingDurationSeconds = 60
	execution := profilerevidence.ExecutionEvidence{
		Intent: intent,
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
		Runtime: profilingrun.IdleRuntimeResult{
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
		!observation.ModelLoadedBeforeSettling || !observation.SettlingWindowVerified ||
		!observation.ModelResidentAtEnd || observation.InferenceCountAfterModelLoad != 0 ||
		!observation.WindowStart.Equal(started) || !observation.WindowEnd.Equal(started.Add(30*time.Second)) {
		t.Fatalf("projected idle observation = %#v", observation)
	}
}

func TestThermalStateProjectsOnlyObservedWindowFacts(t *testing.T) {
	start := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	sample := func(offset time.Duration, thermal *metricobserver.ThermalReading) metricobserver.Sample {
		return metricobserver.Sample{ObservedAt: start.Add(offset), Watts: 4, Thermal: thermal}
	}
	direct := func(active bool) *metricobserver.ThermalReading {
		return &metricobserver.ThermalReading{
			ObservationAvailable: admissionBool(true), Active: admissionBool(active),
		}
	}
	counters := func(value uint64) *metricobserver.ThermalReading {
		return &metricobserver.ThermalReading{
			ObservationAvailable: admissionBool(true), CPUCoreThrottleEvents: admissionUint(value),
		}
	}
	tests := []struct {
		name       string
		samples    []metricobserver.Sample
		wantKnown  bool
		wantActive bool
	}{
		{name: "direct stable", samples: []metricobserver.Sample{sample(time.Second, direct(false)), sample(2*time.Second, direct(false))}, wantKnown: true},
		{name: "direct active", samples: []metricobserver.Sample{sample(time.Second, direct(false)), sample(2*time.Second, direct(true))}, wantKnown: true, wantActive: true},
		{name: "counter stable", samples: []metricobserver.Sample{sample(time.Second, counters(7)), sample(2*time.Second, counters(7))}, wantKnown: true},
		{name: "counter increase", samples: []metricobserver.Sample{sample(time.Second, counters(7)), sample(2*time.Second, counters(8))}, wantKnown: true, wantActive: true},
		{name: "counter reset", samples: []metricobserver.Sample{sample(time.Second, counters(7)), sample(2*time.Second, counters(1))}},
		{
			name: "diagnostic issue does not override direct evidence",
			samples: []metricobserver.Sample{
				sample(time.Second, &metricobserver.ThermalReading{
					ObservationAvailable: admissionBool(true), Active: admissionBool(false),
					Issues: []metricobserver.MetricIssue{{Metric: "device_thermal_max_celsius", Reason: metricobserver.FailureReasonInvalidReading}},
				}),
				sample(2*time.Second, direct(false)),
			},
			wantKnown: true,
		},
		{name: "missing thermal", samples: []metricobserver.Sample{sample(time.Second, nil), sample(2*time.Second, nil)}},
		{name: "outside window ignored", samples: []metricobserver.Sample{sample(-time.Second, direct(true)), sample(time.Second, direct(false))}, wantKnown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			known, active := thermalState(test.samples, start, start.Add(3*time.Second))
			if known != test.wantKnown || active != test.wantActive {
				t.Fatalf("thermalState() = %t, %t; want %t, %t", known, active, test.wantKnown, test.wantActive)
			}
		})
	}
}

func admissionBool(value bool) *bool     { return &value }
func admissionUint(value uint64) *uint64 { return &value }

func TestRunIdentitiesSeparateExecutionStateFromMeasurementProtocol(t *testing.T) {
	t.Parallel()
	intent := admissionIntentWithWorkloadContract()
	base, err := ProjectRunIdentities(intent, "node-a")
	if err != nil {
		t.Fatal(err)
	}

	batch := intent
	batch.Measurement.Batch = 8
	batchIdentities, err := ProjectRunIdentities(batch, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if batchIdentities != base {
		t.Fatalf("batch changed identity family: %#v != %#v", batchIdentities, base)
	}

	idle := intent
	idle.Measurement.WorkloadMode = "idle-model-loaded"
	idle.Measurement.InputGenerator = "none"
	idle.Measurement.OutputValidation = "none"
	idle.Measurement.WarmupIterations = 0
	idle.Measurement.DurationSeconds = 60
	idle.Metrics.DurationSeconds = 61
	idleIdentities, err := ProjectRunIdentities(idle, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if idleIdentities.StateIdentity != base.StateIdentity ||
		idleIdentities.MeasurementProtocolIdentity == base.MeasurementProtocolIdentity {
		t.Fatalf("idle split = %#v, active = %#v", idleIdentities, base)
	}

	observer := intent
	observer.Images.MetricObserver = "registry.example/metric-observer@sha256:" + strings.Repeat("b", 64)
	observerIdentities, err := ProjectRunIdentities(observer, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if observerIdentities.StateIdentity != base.StateIdentity ||
		observerIdentities.MeasurementProtocolIdentity == base.MeasurementProtocolIdentity {
		t.Fatalf("observer split = %#v, base = %#v", observerIdentities, base)
	}

	runtime := intent
	runtime.Images.Runtime = "registry.example/runtime@sha256:" + strings.Repeat("c", 64)
	runtimeIdentities, err := ProjectRunIdentities(runtime, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if runtimeIdentities.StateIdentity == base.StateIdentity {
		t.Fatal("runtime image must change execution-state identity")
	}

	metricSource := intent
	metricSource.Metrics.Endpoint = "http://other-meter"
	metricSourceIdentities, err := ProjectRunIdentities(metricSource, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if metricSourceIdentities.StateIdentity == base.StateIdentity {
		t.Fatal("wall-meter source must change execution-state identity")
	}

	nodeIdentities, err := ProjectRunIdentities(intent, "node-b")
	if err != nil {
		t.Fatal(err)
	}
	if nodeIdentities.StateIdentity == base.StateIdentity {
		t.Fatal("actual Node UID must change execution-state identity")
	}
	hostBound := intent
	hostBound.State.HostContract = &profilingrun.CandidateReportHostContract{
		Libraries: []profilingrun.CandidateReportHostLibrary{{Name: "runtime", HostPath: "/host/runtime", ContainerPath: "/runtime"}},
	}
	hostBoundIdentities, err := ProjectRunIdentities(hostBound, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if hostBoundIdentities.StateIdentity == base.StateIdentity ||
		!strings.HasPrefix(hostBoundIdentities.StateIdentity, stateIdentitySchemaVersion+"@sha256:") {
		t.Fatalf("host contract was not bound to physical state identity: %#v", hostBoundIdentities)
	}
	actuated := intent
	actuated.State.PowerMode = "MODE_15W_6CORE"
	actuated.Actuation = &profilingrun.ActuationContract{
		PowerMode:  actuated.State.PowerMode,
		PolicyName: "jetson-15w",
		Actuator: profilingrun.ActuatorTarget{
			Kind:           profilingrun.ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 2},
		},
	}
	actuatedIdentities, err := ProjectRunIdentities(actuated, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if actuatedIdentities.StateIdentity == base.StateIdentity ||
		!strings.HasPrefix(actuatedIdentities.StateIdentity, stateIdentitySchemaVersion+"@sha256:") ||
		actuatedIdentities.MeasurementProtocolIdentity != base.MeasurementProtocolIdentity {
		t.Fatalf("actuation was not isolated to physical state identity: %#v", actuatedIdentities)
	}
	if !strings.HasPrefix(base.StateIdentity, stateIdentitySchemaVersion+"@sha256:") ||
		!strings.HasPrefix(base.MeasurementProtocolIdentity, measurementProtocolIdentitySchemaVersion+"@sha256:") {
		t.Fatalf("unversioned identities: %#v", base)
	}
}

func admissionIntent() profilingrun.Intent {
	return profilingrun.Intent{
		State: profilingrun.ExecutionState{
			DeviceClass: "lattepanda", Model: "mobilenet",
			RuntimeFamily: "onnxruntime", Backend: "CPUExecutionProvider", PowerMode: "fixed",
		},
		Images:     profilingrun.Images{Runtime: "runtime", Artifact: "artifact", MetricObserver: "observer"},
		TargetNode: profilingrun.NodeSnapshot{Name: "lattepanda", UID: "node-uid"},
		CPU:        profilingrun.CPUContract{Policy: "allocatable", PolicyVersion: "v1", Limit: "4"},
		Measurement: profilingrun.MeasurementContract{
			Batch: 2, WarmupIterations: 20, DurationSeconds: 30,
			WorkloadMode: "saturated", InputGenerator: "seeded", OutputValidation: "shape",
		},
		Metrics: profilingrun.MetricContract{
			SourceKind: "edge-metrics", NodeName: "lattepanda", Endpoint: "http://metrics", PowerMetric: "watts",
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

func acceptedActiveFixture(t *testing.T) (profilerevidence.VerifiedEvidence, FrozenPolicy) {
	t.Helper()
	intentBytes, err := os.ReadFile("../evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(intentBytes, &intent); err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	intent.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat("d", 64)
	intent.Workload = profilingrun.CurrentWorkloadContract()
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}

	candidateBytes, err := os.ReadFile("../evidence/testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := profilingrun.DecodeCandidateReport(candidateBytes)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	podUID := "run-pod"
	targetDuration := float64(intent.Measurement.DurationSeconds)
	samples := make([]metricobserver.Sample, 30)
	for index := range samples {
		samples[index] = metricobserver.Sample{
			ObservedAt: started.Add(500*time.Millisecond + time.Duration(index)*time.Second),
			Watts:      5.2,
			Thermal: &metricobserver.ThermalReading{
				ObservationAvailable: admissionBool(true),
				Active:               admissionBool(false),
			},
		}
	}
	workload := workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: intent.TargetNode.Name,
			NodeUID:  intent.TargetNode.UID,
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
			Phase: "Running", Images: []string{intent.Images.Runtime},
		}},
		FinalActivePods: []workloadobserver.Pod{{
			Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
			Phase: "Running", Images: []string{intent.Images.Runtime},
		}},
		Continuous: true,
	}
	raw := profilerevidence.RawMeasurement{
		SchemaVersion: profilerevidence.RawMeasurementSchemaVersion,
		PodUID:        podUID,
		Runtime: profilingrun.RuntimeResult{
			SchemaVersion:             profilingrun.RuntimeResultSchemaVersion,
			Status:                    "Succeeded",
			ExperimentID:              podUID,
			NodeName:                  intent.TargetNode.Name,
			Model:                     intent.State.Model,
			Architecture:              "x86_64",
			Runtime:                   intent.State.RuntimeFamily,
			RuntimeVersion:            "1.16.0",
			Backend:                   intent.State.Backend,
			Provider:                  intent.State.Backend,
			AvailableProviders:        []string{intent.State.Backend},
			BatchSize:                 intent.Measurement.Batch,
			WarmupIterations:          intent.Measurement.WarmupIterations,
			WarmupCompletedAt:         started.Add(-time.Second).Format(time.RFC3339Nano),
			InferenceCount:            2,
			MeasurementMode:           "duration",
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30,
			TargetDurationSeconds:     &targetDuration,
			OutputValidation: profilingrun.OutputValidation{
				Method: intent.Measurement.OutputValidation, Passed: true,
				OutputShapes: [][]int{{intent.Measurement.Batch, 1000}},
			},
			CPUContract: profilingrun.RuntimeCPU{
				CgroupCPUMax: "400000 100000", ORTIntraOpThreads: intent.CPU.ORTIntraOpThreads,
				ORTInterOpThreads: intent.CPU.ORTInterOpThreads,
				AffinityCPUCount:  intent.CPU.ORTIntraOpThreads,
				CPUSetEffective:   "0-3",
				CgroupCPUStatDelta: map[string]int{
					"user_usec": 1, "system_usec": 2,
				},
			},
			DerivedContract: profilingrun.DerivedContract{
				Backend: intent.State.Backend, Provider: intent.State.Backend,
				NodeSnapshot: intent.TargetNode,
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
		Workload: &workload,
	}
	rawBytes, err := raw.CanonicalJSON(intent)
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
		Intent:        intent, CandidateReport: candidate, RawMeasurement: rawIdentity,
		Terminal: profilerevidence.TerminalFacts{
			Job: profilerevidence.JobTerminalFacts{
				Namespace: "chill-system", Name: "profiling-run", UID: "job-uid",
				Succeeded: 1, CompletionTime: completed,
			},
			Pod: profilerevidence.PodTerminalFacts{
				Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID,
				ControllerOwnerUID: "job-uid", NodeName: intent.TargetNode.Name, Phase: "Succeeded",
			},
			Node: profilerevidence.NodeTerminalFacts{Name: intent.TargetNode.Name, UID: intent.TargetNode.UID},
			Containers: []profilerevidence.ContainerTerminal{
				container("metric-observer", intent.Images.MetricObserver),
				container("workload-observer", intent.Images.WorkloadObserver),
				container("runtime", intent.Images.Runtime),
				container("artifact", intent.Images.Artifact),
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

	return verified, activeFixturePolicy(t, "accepted-active", 2)
}

func activeFixturePolicy(t *testing.T, name string, minimumSamples int) FrozenPolicy {
	t.Helper()
	policyCatalog := fmt.Sprintf(`
schemaVersion: chill.dacs.io/profiler-admission-policies.v1alpha1
policies:
  - name: %s
    workloadMode: saturated-serial-fixed-batch
    minimumSamples: %d
    maximumGapSeconds: 2
    maximumBoundaryDistanceSeconds: 1
    minimumCoverage: 0.9
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
`, name, minimumSamples)
	policy, err := FreezeConfiguredPolicy([]byte(policyCatalog), name, PolicySource{
		Kind: ConfigMapPolicySourceKind, Namespace: "chill-system", Name: "admission-policies",
		UID: "policy-uid", ResourceVersion: "1", Key: "admission-policies.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
