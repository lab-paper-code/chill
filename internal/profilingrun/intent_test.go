package profilingrun

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func decodeTestIntent(payload []byte) (Intent, error) {
	var intent Intent
	err := json.Unmarshal(payload, &intent)
	return intent, err
}

func TestIntentDigestIsDeterministic(t *testing.T) {
	intent := validIntent()
	first, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("unstable digest %q %q", first, second)
	}
}

func TestIntentRejectsMutableImage(t *testing.T) {
	intent := validIntent()
	intent.Images.Runtime = "registry/runtime:latest"
	if err := intent.Validate(); err == nil {
		t.Fatal("expected mutable image rejection")
	}
}

func TestIntentRejectsMultipleMeasurementRepetitions(t *testing.T) {
	intent := validIntent()
	intent.Measurement.Repetitions = 2
	if err := intent.Validate(); err == nil {
		t.Fatal("expected first-path repetition rejection")
	}
}

func TestCurrentIntentRequiresCleanArtifactPath(t *testing.T) {
	for _, artifactPath := range []string{"", "/", "artifact/model.onnx", "/artifact/../model.onnx"} {
		intent := validIntent()
		intent.State.ArtifactPath = artifactPath
		if err := intent.Validate(); err == nil {
			t.Fatalf("expected artifact path %q rejection", artifactPath)
		}
	}
}

func TestLegacyIntentRemainsValidWithoutArtifactPath(t *testing.T) {
	intent := validIntent()
	intent.SchemaVersion = legacyIntentSchemaVersion
	intent.State.ArtifactPath = ""
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "artifactPath") {
		t.Fatalf("legacy canonical intent gained artifactPath: %s", payload)
	}
}

func TestRetainedLegacyEvidenceIntentDigestRemainsStable(t *testing.T) {
	payload, err := os.ReadFile("../../spikes/profilingrun/observations/raw/sha256-165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42.json")
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		IntentDigest string `json:"intentDigest"`
		Intent       Intent `json:"intent"`
	}
	if err := json.Unmarshal(payload, &evidence); err != nil {
		t.Fatal(err)
	}
	digest, err := evidence.Intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != evidence.IntentDigest {
		t.Fatalf("retained legacy intent digest changed: want %s, got %s", evidence.IntentDigest, digest)
	}
}

func TestIntentRejectsUnknownImageField(t *testing.T) {
	payload, err := validIntent().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.Replace(string(payload), `"metricObserver":`, `"unknown":true,"metricObserver":`, 1))
	if _, err := decodeTestIntent(payload); err == nil {
		t.Fatal("expected unknown image field rejection")
	}
}

func TestIntentRejectsCPURequestAboveFrozenLimit(t *testing.T) {
	intent := validIntent()
	intent.CPU.Request = "5"
	if err := intent.Validate(); err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("expected CPU request/limit rejection, got %v", err)
	}
}

func TestIntentRejectsCompatibilityAndPolicyDrift(t *testing.T) {
	tests := []func(*Intent){
		func(i *Intent) { i.Candidate.Verdict = "Unknown" },
		func(i *Intent) { i.TargetNode.ResourceVersion = "" },
		func(i *Intent) { i.CPU.ORTIntraOpThreads = 2 },
		func(i *Intent) { i.CPU.ExclusivityClaimed = true },
		func(i *Intent) { i.Metrics.NodeName = "other" },
	}
	for _, mutate := range tests {
		intent := validIntent()
		mutate(&intent)
		if err := intent.Validate(); err == nil {
			t.Fatal("expected validation failure")
		}
	}
}

func validIntent() Intent {
	d := "sha256:" + strings.Repeat("a", 64)
	image := "registry.local/chill/image@" + d
	return Intent{
		SchemaVersion: SchemaVersion,
		Candidate: CandidateIdentity{
			ReportDigest:             d,
			ModelSpecContentDigest:   d,
			DeviceClassContentDigest: d,
			RuntimeDeclarationDigest: d,
			ExecutionPath:            "ort-cpu",
			Verdict:                  "Compatible",
		},
		State: ExecutionState{
			DeviceClass:    "lattepanda-3-delta-8g",
			Model:          "mobilenet-v2-050",
			Artifact:       "canonical-onnx",
			ArtifactDigest: d,
			ArtifactPath:   "/artifact/mobilenet-v2-050.onnx",
			RuntimeFamily:  "onnxruntime",
			Backend:        "CPUExecutionProvider",
			PowerMode:      "fixed",
		},
		Images: Images{Runtime: image, Artifact: image, MetricObserver: image},
		TargetNode: NodeSnapshot{
			Name:            "lattepanda",
			UID:             "uid",
			ResourceVersion: "1",
			Architecture:    "amd64",
			AllocatableCPU:  "4",
		},
		CPU: CPUContract{
			Policy:            "AllocatableCPUQuota",
			PolicyVersion:     "v1",
			Request:           "100m",
			Limit:             "4",
			ORTIntraOpThreads: 4,
			ORTInterOpThreads: 1,
		},
		Measurement: MeasurementContract{
			Batch:            1,
			WarmupIterations: 20,
			DurationSeconds:  30,
			Repetitions:      1,
			WorkloadMode:     "saturated-serial-fixed-batch",
			InputGenerator:   "numpy-default-rng-seed-0-fp32-v1",
			OutputValidation: "nonempty-shape-batch-v1",
		},
		Metrics: MetricContract{
			SourceKind:                 "edge-metrics",
			NodeName:                   "lattepanda",
			Endpoint:                   "http://edge-metric.monitoring.svc:9102/metrics",
			PowerMetric:                "shelly_power_total_watts",
			ConfigurationContentDigest: d,
			IntervalMilliseconds:       1000,
			TimeoutMilliseconds:        500,
			DurationSeconds:            31,
		},
	}
}
