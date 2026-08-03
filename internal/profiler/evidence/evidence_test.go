package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestRawMeasurementCanonicalJSONAndValidation(t *testing.T) {
	intent := loadIntent(t)
	raw := validRaw(t, intent)

	first, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("raw measurement encoding is not deterministic")
	}
	var decodedRaw RawMeasurement
	if err := json.Unmarshal(first, &decodedRaw); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := decodedRaw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(roundTrip) {
		t.Fatal("raw measurement does not survive an encode/decode round trip")
	}
	if raw.Metrics.StartedAt.Location() == time.UTC {
		t.Fatal("canonical encoding mutated the caller's power timestamps")
	}

	drifted := raw
	drifted.Metrics.Source.NodeName = "other"
	if err := drifted.Validate(intent); err == nil {
		t.Fatal("expected metric source identity drift rejection")
	}

	drifted = raw
	drifted.Metrics.Summary.Attempts++
	if err := drifted.Validate(intent); err == nil {
		t.Fatal("expected inconsistent metric summary rejection")
	}

	drifted = raw
	drifted.Metrics.Source.ThermalMetrics = nil
	if err := drifted.Validate(intent); err == nil {
		t.Fatal("expected missing thermal metric identity rejection")
	}
}

func TestRetainedRawMeasurementWireShapeRemainsReadable(t *testing.T) {
	intent := loadIntent(t)
	raw := validRaw(t, intent)
	raw.SchemaVersion = legacyRawMeasurementSchemaVersion
	raw.Workload = nil
	raw.Metrics.Source.ThermalMetrics = nil
	for index := range raw.Metrics.Samples {
		raw.Metrics.Samples[index].Thermal = nil
	}
	payload, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"power":`) || strings.Contains(string(payload), `"metrics":`) {
		t.Fatalf("retained raw measurement wire shape = %s", payload)
	}
	var decoded RawMeasurement
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(intent); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedMetricRawMeasurementRemainsReadableWithoutWorkloadObservation(t *testing.T) {
	intent := loadIntent(t)
	raw := validRaw(t, intent)
	raw.SchemaVersion = metricRawMeasurementSchemaVersion
	raw.Workload = nil
	payload, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"metrics":`) || strings.Contains(string(payload), `"workload":`) {
		t.Fatalf("retained metric raw measurement wire shape = %s", payload)
	}
	var decoded RawMeasurement
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(intent); err != nil {
		t.Fatal(err)
	}
}

func TestRawMeasurementRequiresOverlappingRuntimeAndMetricWindows(t *testing.T) {
	intent := loadIntent(t)
	raw := validRaw(t, intent)
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
			name:       "partial overlap remains an admission decision",
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
			err := candidate.Validate(intent)
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestCurrentRawMeasurementBindsWorkloadObservationToIntentAndRuntime(t *testing.T) {
	intent := loadIntent(t)
	raw := validRaw(t, intent)
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
		{name: "policy", mutate: func(candidate *RawMeasurement) {
			candidate.Workload.PolicyDigest = "sha256:" + strings.Repeat("0", 64)
		}, match: "policy digest"},
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
			if err := candidate.Validate(intent); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Validate() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestIdleRawMeasurementBindsRuntimeDeclaredWindowAndObservers(t *testing.T) {
	intent := idleIntent(t)
	raw := validIdleRaw(t, intent)
	first, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	var decoded IdleRawMeasurement
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	second, err := decoded.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("idle raw measurement does not survive canonical round trip")
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
			if err := candidate.Validate(intent); err == nil {
				t.Fatal("invalid idle raw measurement was accepted")
			}
		})
	}
}

func TestIdleRawMeasurementStrictDecodeRejectsUnknownFields(t *testing.T) {
	intent := idleIntent(t)
	payload, err := validIdleRaw(t, intent).CanonicalJSON(intent)
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
	intent := loadIntent(t)
	raw := validRaw(t, intent)
	evidence := validExecutionEvidence(t, intent, raw)

	first, err := evidence.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	reordered := evidence
	reordered.Terminal.Containers = reverseContainers(evidence.Terminal.Containers)
	reordered.Terminal.NodeWorkload.Pods = reversePods(evidence.Terminal.NodeWorkload.Pods)
	reordered.FinalizedAt = evidence.FinalizedAt.UTC()
	reordered.Terminal.NodeWorkload.ObservedAt = evidence.Terminal.NodeWorkload.ObservedAt.UTC()
	second, err := reordered.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("execution evidence encoding depends on observation order or time zone")
	}
	var decoded ExecutionEvidence
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := decoded.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(roundTrip) {
		t.Fatal("execution evidence does not survive an encode/decode round trip")
	}

	invalid := evidence
	invalid.RawMeasurement.Digest = "sha256:" + strings.Repeat("0", 64)
	if err := invalid.Validate(raw); err == nil {
		t.Fatal("expected raw object identity mismatch rejection")
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
	driftedBytes, err := driftedRaw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	driftedEvidence := evidence
	driftedSum := sha256.Sum256(driftedBytes)
	driftedEvidence.RawMeasurement.Digest = "sha256:" + hex.EncodeToString(driftedSum[:])
	driftedEvidence.RawMeasurement.Size = int64(len(driftedBytes))
	if err := driftedEvidence.Validate(driftedRaw); err == nil || !strings.Contains(err.Error(), "terminal Kubernetes") {
		t.Fatalf("expected workload-to-terminal identity rejection, got %v", err)
	}

	invalid = evidence
	invalid.Terminal.NodeWorkload.Pods = invalid.Terminal.NodeWorkload.Pods[:1]
	if err := invalid.Validate(raw); err == nil {
		t.Fatal("expected missing Run Pod workload observation rejection")
	}
}

func TestExecutionEvidenceValidatesIdleRawObjectIdentity(t *testing.T) {
	intent := idleIntent(t)
	raw := validIdleRaw(t, intent)
	execution := validIdleExecutionEvidence(t, intent, raw)
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
	invalid.RawMeasurement.Size++
	if err := invalid.ValidateIdle(raw); err == nil {
		t.Fatal("idle execution evidence accepted a drifted raw object identity")
	}
}

func loadIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
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

func validRaw(t *testing.T, intent profilingrun.Intent) RawMeasurement {
	t.Helper()
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("KST", 9*60*60)
	started := time.Date(2026, time.July, 16, 12, 0, 0, 0, location)
	podUID := "profiling-pod-uid"
	targetDuration := float64(intent.Measurement.DurationSeconds)
	raw := RawMeasurement{
		SchemaVersion: RawMeasurementSchemaVersion,
		IntentDigest:  digest,
		PodUID:        podUID,
		Runtime: profilingrun.RuntimeResult{
			SchemaVersion:             profilingrun.RuntimeResultSchemaVersion,
			Status:                    "Succeeded",
			SweepID:                   digest,
			ExperimentID:              podUID,
			NodeName:                  intent.TargetNode.Name,
			Model:                     intent.State.Model,
			ArtifactDigest:            intent.State.ArtifactDigest,
			Architecture:              "x86_64",
			Runtime:                   intent.State.RuntimeFamily,
			RuntimeVersion:            "1.16.0",
			Provider:                  intent.State.Backend,
			AvailableProviders:        []string{intent.State.Backend},
			BatchSize:                 intent.Measurement.Batch,
			WarmupIterations:          intent.Measurement.WarmupIterations,
			WarmupCompletedAt:         started.Add(-time.Second).UTC().Format(time.RFC3339Nano),
			InferenceCount:            2,
			MeasurementMode:           "duration",
			Repetitions:               intent.Measurement.Repetitions,
			MeasurementStartedAt:      started.UTC().Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).UTC().Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30,
			TargetDurationSeconds:     &targetDuration,
			OutputValidation: profilingrun.OutputValidation{
				Method:       intent.Measurement.OutputValidation,
				Passed:       true,
				OutputShapes: [][]int{{intent.Measurement.Batch, 1000}},
			},
			CPUContract: profilingrun.RuntimeCPU{
				CgroupCPUMax:       "400000 100000",
				ORTIntraOpThreads:  intent.CPU.ORTIntraOpThreads,
				ORTInterOpThreads:  intent.CPU.ORTInterOpThreads,
				AffinityCPUCount:   intent.CPU.ORTIntraOpThreads,
				CPUSetEffective:    "0-3",
				CgroupCPUStatDelta: map[string]int{"user_usec": 1, "system_usec": 2},
			},
			DerivedContract: profilingrun.DerivedContract{
				RunIntentDigest: digest,
				NodeSnapshot:    intent.TargetNode,
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
				Kind:     intent.Metrics.SourceKind,
				NodeName: intent.Metrics.NodeName,
				Endpoint: intent.Metrics.Endpoint,
				Metric:   intent.Metrics.PowerMetric,
				ThermalMetrics: &metricobserver.ThermalMetricIdentity{
					ObservationAvailable:      "device_thermal_throttle_observation_available",
					Active:                    "device_thermal_throttle_active",
					CPUCoreThrottleEvents:     "device_thermal_cpu_core_throttle_event_count",
					CPUPackageThrottleEvents:  "device_thermal_cpu_package_throttle_event_count",
					Latched:                   "device_thermal_throttle_latched",
					MaximumTemperatureCelsius: "device_thermal_max_celsius",
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
	policyDigest, err := intent.Workload.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runPod := workloadobserver.Pod{
		Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID, Phase: "Running",
		Images: []string{intent.Images.Runtime},
	}
	raw.Workload = &workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: intent.TargetNode.Name, NodeUID: intent.TargetNode.UID,
		},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system", JobUID: "job-uid", PodName: "profiling-run-pod", PodUID: podUID,
		},
		PolicyDigest:      policyDigest,
		CoverageStartedAt: started.Add(-time.Second), CoverageEndedAt: started.Add(31 * time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{runPod}, FinalActivePods: []workloadobserver.Pod{runPod},
		Continuous: true,
	}
	return raw
}

func idleIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	intent := loadIntent(t)
	intent.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, Repetitions: 1,
		WorkloadMode:   profilingrun.IdleModelLoadedWorkloadMode,
		InputGenerator: profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
	}
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func validIdleRaw(t *testing.T, intent profilingrun.Intent) IdleRawMeasurement {
	t.Helper()
	activeShape := validRaw(t, intent)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	started := activeShape.Metrics.StartedAt
	return IdleRawMeasurement{
		SchemaVersion: IdleRawMeasurementSchemaVersion,
		IntentDigest:  digest,
		PodUID:        activeShape.PodUID,
		Runtime: profilingrun.IdleRuntimeResult{
			SchemaVersion: profilingrun.IdleRuntimeResultSchemaVersion, Status: "Succeeded",
			IntentDigest: digest, PodUID: activeShape.PodUID, NodeName: intent.TargetNode.Name,
			Model: intent.State.Model, ArtifactDigest: intent.State.ArtifactDigest,
			Architecture: "x86_64", Runtime: intent.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Provider: intent.State.Backend, AvailableProviders: []string{intent.State.Backend},
			ModelLoadCompletedAt:      started.Add(-61 * time.Second).UTC().Format(time.RFC3339Nano),
			SettlingStartedAt:         started.Add(-60 * time.Second).UTC().Format(time.RFC3339Nano),
			SettlingCompletedAt:       started.UTC().Format(time.RFC3339Nano),
			MeasurementStartedAt:      started.UTC().Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).UTC().Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true,
			CPUContract: profilingrun.RuntimeCPU{
				CgroupCPUMax: "400000 100000", ORTIntraOpThreads: intent.CPU.ORTIntraOpThreads,
				ORTInterOpThreads: intent.CPU.ORTInterOpThreads,
				AffinityCPUCount:  intent.CPU.ORTIntraOpThreads, CPUSetEffective: "0-3",
			},
			DerivedContract: profilingrun.DerivedContract{RunIntentDigest: digest, NodeSnapshot: intent.TargetNode},
		},
		Metrics:  activeShape.Metrics,
		Workload: activeShape.Workload,
	}
}

func validExecutionEvidence(
	t *testing.T,
	intent profilingrun.Intent,
	raw RawMeasurement,
) ExecutionEvidence {
	t.Helper()
	rawBytes, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	return validExecutionEvidenceBytes(t, intent, raw.PodUID, rawBytes)
}

func validIdleExecutionEvidence(
	t *testing.T,
	intent profilingrun.Intent,
	raw IdleRawMeasurement,
) ExecutionEvidence {
	t.Helper()
	rawBytes, err := raw.CanonicalJSON(intent)
	if err != nil {
		t.Fatal(err)
	}
	return validExecutionEvidenceBytes(t, intent, raw.PodUID, rawBytes)
}

func validExecutionEvidenceBytes(
	t *testing.T,
	intent profilingrun.Intent,
	podUID string,
	rawBytes []byte,
) ExecutionEvidence {
	t.Helper()
	sum := sha256.Sum256(rawBytes)
	candidate, err := os.ReadFile("testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	location := time.FixedZone("KST", 9*60*60)
	completed := time.Date(2026, time.July, 16, 12, 1, 0, 0, location)
	container := func(name, image string) ContainerTerminal {
		return ContainerTerminal{
			Name:           name,
			RequestedImage: image,
			ImageID:        "containerd://" + imageDigest(image),
			Reason:         "Completed",
			StartedAt:      completed.Add(-time.Minute),
			FinishedAt:     completed.Add(-time.Second),
		}
	}
	return ExecutionEvidence{
		SchemaVersion:        ExecutionEvidenceSchemaVersion,
		IntentDigest:         digest,
		Intent:               intent,
		CandidateReportBytes: candidate,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store:  "chill-evidence",
			Key:    "runs/run-uid/raw.json",
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
			Size:   int64(len(rawBytes)),
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
				NodeName:           intent.TargetNode.Name,
				Phase:              "Succeeded",
			},
			Node: NodeTerminalFacts{Name: intent.TargetNode.Name, UID: intent.TargetNode.UID},
			Containers: []ContainerTerminal{
				container("metric-observer", intent.Images.MetricObserver),
				container("workload-observer", intent.Images.WorkloadObserver),
				container("runtime", intent.Images.Runtime),
				container("artifact", intent.Images.Artifact),
			},
			NodeWorkload: NodeWorkload{
				ObservedAt: completed.Add(time.Second),
				Pods: []WorkloadPod{
					{Namespace: "monitoring", Name: "edge-metric", UID: "edge-metric-uid", Phase: "Running", Images: []string{"image-b", "image-a"}},
					{Namespace: "chill-system", Name: "profiling-run-pod", UID: podUID, Phase: "Succeeded", Images: []string{intent.Images.Runtime}},
				},
			},
		},
		FinalizedAt: completed.Add(2 * time.Second),
	}
}

func reverseContainers(input []ContainerTerminal) []ContainerTerminal {
	result := append([]ContainerTerminal(nil), input...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func reversePods(input []WorkloadPod) []WorkloadPod {
	result := append([]WorkloadPod(nil), input...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
