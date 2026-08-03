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

func TestCurrentIntentCanonicalBytesAndDigestRemainStable(t *testing.T) {
	payload, err := os.ReadFile("testdata/intent-v1alpha6.json")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := decodeTestIntent(payload)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(canonical)+"\n" {
		t.Fatalf("current intent canonical bytes changed:\nwant %s\n got %s", payload, canonical)
	}
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	const wantDigest = "sha256:a45a0a91f397b863fca9eb124419233bc240e5e5eb9395055ffa09640af4523b"
	if digest != wantDigest {
		t.Fatalf("current intent digest changed: want %s, got %s", wantDigest, digest)
	}
}

func TestIntentRejectsMutableImage(t *testing.T) {
	intent := validIntent()
	intent.Images.Runtime = "registry/runtime:latest"
	if err := intent.Validate(); err == nil {
		t.Fatal("expected mutable image rejection")
	}
}

func TestCurrentIntentFreezesGenericWorkloadObservationContract(t *testing.T) {
	intent := validIntent()
	digest, err := intent.Workload.Digest()
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("contract digest = %q, error = %v", digest, err)
	}
	keys, err := intent.Workload.LabelKeys()
	if err != nil || strings.Join(keys, ",") !=
		"app.kubernetes.io/component,app.kubernetes.io/name,edge.dacs.io/workload-role" {
		t.Fatalf("contract label keys = %#v, error = %v", keys, err)
	}
	intent.Workload.SchemaVersion = "unknown"
	if err := intent.Validate(); err == nil || !strings.Contains(err.Error(), "current workload") {
		t.Fatalf("invalid workload contract error = %v", err)
	}
}

func TestMeasurementWorkloadRequiresCHILLRoleAndJobOwner(t *testing.T) {
	labels := map[string]string{MeasurementWorkloadLabelKey: MeasurementWorkloadLabelValue}
	if !IsMeasurementWorkload("Job", labels) || IsMeasurementWorkload("DaemonSet", labels) ||
		IsMeasurementWorkload("Job", map[string]string{}) {
		t.Fatal("measurement workload identity was not exact")
	}
}

func TestIntentRejectsMultipleMeasurementRepetitions(t *testing.T) {
	intent := validIntent()
	intent.Measurement.Repetitions = 2
	if err := intent.Validate(); err == nil {
		t.Fatal("expected first-path repetition rejection")
	}
}

func TestCurrentIntentValidatesModelLoadedIdleProtocol(t *testing.T) {
	idle := validIntent()
	idle.Measurement = MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, Repetitions: 1,
		WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator,
		OutputValidation: NoOutputValidation,
	}
	if err := idle.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Intent){
		func(i *Intent) { i.Measurement.Batch = 2 },
		func(i *Intent) { i.Measurement.WarmupIterations = 1 },
		func(i *Intent) { i.Measurement.SettlingDurationSeconds = 0 },
		func(i *Intent) { i.Measurement.InputGenerator = "seeded" },
		func(i *Intent) { i.Measurement.OutputValidation = "shape" },
	} {
		candidate := idle
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid model-loaded idle protocol was accepted")
		}
	}
}

func TestRetainedV1Alpha5IntentCannotGainSettlingField(t *testing.T) {
	intent := validIntent()
	intent.SchemaVersion = previousCurrentIntentVersion
	payload, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "settlingDurationSeconds") {
		t.Fatalf("retained v1alpha5 intent gained settling field: %s", payload)
	}
	intent.Measurement.SettlingDurationSeconds = 1
	if err := intent.Validate(); err == nil {
		t.Fatal("retained v1alpha5 intent accepted the v1alpha6 field")
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
		Images: Images{
			Runtime: image, Artifact: image, MetricObserver: image, WorkloadObserver: image,
		},
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
		Workload: CurrentWorkloadContract(),
	}
}
