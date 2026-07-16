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
	"github.com/lab-paper-code/chill/internal/powerobserver"
	"github.com/lab-paper-code/chill/internal/profilingrun"
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
	if raw.Power.StartedAt.Location() == time.UTC {
		t.Fatal("canonical encoding mutated the caller's power timestamps")
	}

	drifted := raw
	drifted.Power.Source.NodeName = "other"
	if err := drifted.Validate(intent); err == nil {
		t.Fatal("expected power source identity drift rejection")
	}

	drifted = raw
	drifted.Power.Summary.Attempts++
	if err := drifted.Validate(intent); err == nil {
		t.Fatal("expected inconsistent power summary rejection")
	}
}

func TestRawMeasurementRequiresOverlappingRuntimeAndPowerWindows(t *testing.T) {
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
			candidate.Power.StartedAt = test.powerStart
			candidate.Power.EndedAt = test.powerEnd
			candidate.Power.Samples = nil
			candidate.Power.Summary.Attempts = 0
			candidate.Power.Summary.SuccessfulSamples = 0
			err := candidate.Validate(intent)
			if (err != nil) != test.wantError {
				t.Fatalf("Validate() error = %v, wantError=%v", err, test.wantError)
			}
		})
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

	invalid = evidence
	invalid.Terminal.NodeWorkload.Pods = invalid.Terminal.NodeWorkload.Pods[:1]
	if err := invalid.Validate(raw); err == nil {
		t.Fatal("expected missing Run Pod workload observation rejection")
	}
}

func loadIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	payload, err := os.ReadFile("../../../spikes/profilingrun/fixtures/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
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
	return RawMeasurement{
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
		Power: powerobserver.Result{
			Source: powerobserver.SourceIdentity{
				Kind:     intent.Power.SourceKind,
				NodeName: intent.Power.NodeName,
				Endpoint: intent.Power.Endpoint,
				Metric:   intent.Power.Metric,
			},
			StartedAt: started,
			EndedAt:   started.Add(31 * time.Second),
			Samples: []powerobserver.Sample{{
				ObservedAt: started.Add(time.Second),
				Watts:      5.2,
			}},
			Failures: []powerobserver.Failure{},
			Summary: powerobserver.Summary{
				Attempts:          1,
				SuccessfulSamples: 1,
				Failures:          0,
			},
		},
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
	sum := sha256.Sum256(rawBytes)
	candidate, err := os.ReadFile("../../../spikes/profilingrun/fixtures/candidate-report.json")
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
				UID:                raw.PodUID,
				ControllerOwnerUID: "job-uid",
				NodeName:           intent.TargetNode.Name,
				Phase:              "Succeeded",
			},
			Node: NodeTerminalFacts{Name: intent.TargetNode.Name, UID: intent.TargetNode.UID},
			Containers: []ContainerTerminal{
				container("power-observer", intent.Images.PowerObserver),
				container("runtime", intent.Images.Runtime),
				container("artifact", intent.Images.Artifact),
			},
			NodeWorkload: NodeWorkload{
				ObservedAt: completed.Add(time.Second),
				Pods: []WorkloadPod{
					{Namespace: "monitoring", Name: "edge-metric", UID: "edge-metric-uid", Phase: "Running", Images: []string{"image-b", "image-a"}},
					{Namespace: "chill-system", Name: "profiling-run-pod", UID: raw.PodUID, Phase: "Succeeded", Images: []string{intent.Images.Runtime}},
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
