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

func TestRetainedV1Alpha6CanonicalBytesAndDigestRemainStable(t *testing.T) {
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
		t.Fatalf("retained intent canonical bytes changed:\nwant %s\n got %s", payload, canonical)
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

func TestCurrentIntentDigestRemainsStable(t *testing.T) {
	digest, err := validIntent().Digest()
	if err != nil {
		t.Fatal(err)
	}
	const wantDigest = "sha256:d56d3771367f6f5fccd5745bd861da8d1f7a3549d3fe9878ceeb95a9d6d2239d"
	if digest != wantDigest {
		t.Fatalf("current intent digest changed: want %s, got %s", wantDigest, digest)
	}
}

func TestRetainedV1Alpha7IntentDigestRemainsStable(t *testing.T) {
	intent := validIntent()
	intent.SchemaVersion = previousIntentSchemaVersion
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	const wantDigest = "sha256:fd10ce1e3301a912a667f17fea42d4eb44c7e2b6f9da5244c1c835e6302af8a9"
	if digest != wantDigest {
		t.Fatalf("retained v1alpha7 intent digest changed: want %s, got %s", wantDigest, digest)
	}
}

func TestCurrentIntentBindsOptionalHostContractDigest(t *testing.T) {
	intent := validIntent()
	intent.State.HostContractDigest = "sha256:" + strings.Repeat("b", 64)
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}

	retained := intent
	retained.SchemaVersion = previousIntentSchemaVersion
	if err := retained.Validate(); err != nil {
		t.Fatalf("retained v1alpha7 schema rejected host state material: %v", err)
	}
	retained.SchemaVersion = intentSchemaVersionV1Alpha6
	if err := retained.Validate(); err == nil || !strings.Contains(err.Error(), "v1alpha6") {
		t.Fatalf("retained v1alpha6 schema accepted new state material: %v", err)
	}

	intent.State.HostContractDigest = "sha256:short"
	if err := intent.Validate(); err == nil {
		t.Fatal("expected invalid host contract digest rejection")
	}
}

func TestCurrentRKNNLiteIntentOmitsORTThreadSettings(t *testing.T) {
	intent := validIntent()
	intent.Candidate.ExecutionPath = RKNNLiteExecutionPath
	intent.State.RuntimeFamily = RKNNLiteRuntimeFamily
	intent.State.Backend = RKNNLiteBackend
	intent.State.HostContractDigest = "sha256:" + strings.Repeat("b", 64)
	intent.CPU.ORTIntraOpThreads = 0
	intent.CPU.ORTInterOpThreads = 0
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "ortIntraOpThreads") || strings.Contains(string(payload), "ortInterOpThreads") {
		t.Fatalf("RKNNLite intent retained ORT-only CPU settings: %s", payload)
	}
	if IsSupportedCPUORTIntent(intent) {
		t.Fatal("RKNNLite intent was classified as CPU ORT")
	}
	if !IsSupportedRKNNLiteIntent(intent) {
		t.Fatal("current host-bound RKNNLite intent was not classified as supported")
	}

	withORTSettings := intent
	withORTSettings.CPU.ORTIntraOpThreads = 4
	withORTSettings.CPU.ORTInterOpThreads = 1
	if err := withORTSettings.Validate(); err == nil || !strings.Contains(err.Error(), "ONNX Runtime thread") {
		t.Fatalf("RKNNLite intent accepted ORT-only settings: %v", err)
	}
	retained := intent
	retained.SchemaVersion = previousIntentSchemaVersion
	if err := retained.Validate(); err == nil || !strings.Contains(err.Error(), "current intent schema") {
		t.Fatalf("retained intent schema accepted RKNNLite contract: %v", err)
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
