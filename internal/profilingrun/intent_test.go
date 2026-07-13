package profilingrun

import (
	"encoding/json"
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

func TestIntentRejectsCompatibilityAndPolicyDrift(t *testing.T) {
	tests := []func(*Intent){
		func(i *Intent) { i.Candidate.Verdict = "Unknown" },
		func(i *Intent) { i.TargetNode.ResourceVersion = "" },
		func(i *Intent) { i.CPU.ORTIntraOpThreads = 2 },
		func(i *Intent) { i.CPU.ExclusivityClaimed = true },
		func(i *Intent) { i.Power.NodeName = "other" },
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
			RuntimeFamily:  "onnxruntime",
			Backend:        "CPUExecutionProvider",
			PowerMode:      "fixed",
		},
		Images: Images{Runtime: image, Artifact: image, PowerObserver: image},
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
		Power: PowerContract{
			SourceKind:           "EdgeMetricsShelly",
			NodeName:             "lattepanda",
			Endpoint:             "http://edge-metric.monitoring.svc:9102/metrics",
			Metric:               "shelly_power_total_watts",
			IntervalMilliseconds: 1000,
			TimeoutMilliseconds:  500,
			DurationSeconds:      31,
		},
	}
}
