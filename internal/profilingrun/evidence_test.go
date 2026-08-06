package profilingrun

import (
	"strings"
	"testing"
	"time"
)

func TestValidateRuntimeRejectsMaterialAndOutputDrift(t *testing.T) {
	intent := validIntent()
	valid := validRuntimeResult(intent)
	if err := ValidateRuntime(intent, "pod-uid", valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.OutputValidation.Passed = false
	if err := ValidateRuntime(intent, "pod-uid", invalid); err == nil {
		t.Fatal("expected output drift rejection")
	}
	invalid = valid
	invalid.NodeName = "other-node"
	if err := ValidateRuntime(intent, "pod-uid", invalid); err == nil {
		t.Fatal("expected Node drift rejection")
	}
	invalid = valid
	invalid.MeasurementEndedAt = invalid.MeasurementStartedAt
	if err := ValidateRuntime(intent, "pod-uid", invalid); err == nil {
		t.Fatal("expected empty measurement window rejection")
	}
	invalid = valid
	invalid.MeasurementEndedAt = "not-a-time"
	if err := ValidateRuntime(intent, "pod-uid", invalid); err == nil {
		t.Fatal("expected invalid measurement end rejection")
	}

	started, ended, err := valid.MeasurementWindow()
	if err != nil {
		t.Fatal(err)
	}
	if !started.Equal(time.Date(2026, time.January, 1, 0, 0, 1, 0, time.UTC)) ||
		!ended.Equal(time.Date(2026, time.January, 1, 0, 0, 31, 0, time.UTC)) {
		t.Fatalf("measurement window = %s..%s", started, ended)
	}
}

func TestValidateRuntimeRejectsInvalidRepetitionEvidence(t *testing.T) {
	intent := validIntent()
	base := validRuntimeResult(intent)

	for _, test := range []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{name: "wrong sequence", mutate: func(result *RuntimeResult) {
			result.RepetitionResults[0].Repetition = 2
		}},
		{name: "window outside measurement", mutate: func(result *RuntimeResult) {
			result.RepetitionResults[0].MeasurementStartedAt = "2025-12-31T23:59:59Z"
		}},
		{name: "zero latency", mutate: func(result *RuntimeResult) {
			result.RepetitionResults[0].LatencyMilliseconds[0] = 0
		}},
		{name: "target duration drift", mutate: func(result *RuntimeResult) {
			other := 10.0
			result.TargetDurationSeconds = &other
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.RepetitionResults = append([]Repetition(nil), base.RepetitionResults...)
			candidate.RepetitionResults[0].LatencyMilliseconds = append(
				[]float64(nil), base.RepetitionResults[0].LatencyMilliseconds...,
			)
			test.mutate(&candidate)
			if err := ValidateRuntime(intent, "pod-uid", candidate); err == nil {
				t.Fatal("expected invalid repetition evidence rejection")
			}
		})
	}
}

func validRuntimeResult(intent Intent) RuntimeResult {
	digest, _ := intent.Digest()
	targetDuration := float64(intent.Measurement.DurationSeconds)
	return RuntimeResult{
		SchemaVersion: RuntimeResultSchemaVersion, Status: "Succeeded", SweepID: digest,
		ExperimentID: "pod-uid", NodeName: intent.TargetNode.Name, Model: intent.State.Model,
		ArtifactDigest: intent.State.ArtifactDigest, Runtime: intent.State.RuntimeFamily,
		Architecture: "x86_64", RuntimeVersion: "1", Backend: intent.State.Backend, Provider: intent.State.Backend,
		AvailableProviders: []string{intent.State.Backend},
		BatchSize:          1, WarmupIterations: 20, WarmupCompletedAt: "2026-01-01T00:00:00Z",
		InferenceCount: 1, MeasurementMode: "duration", Repetitions: 1,
		MeasurementStartedAt: "2026-01-01T00:00:01Z", MeasurementEndedAt: "2026-01-01T00:00:31Z",
		MeasurementElapsedSeconds: 30, TargetDurationSeconds: &targetDuration,
		OutputValidation: OutputValidation{
			Method: intent.Measurement.OutputValidation, Passed: true, OutputShapes: [][]int{{1, 1000}},
		},
		CPUContract: RuntimeCPU{
			CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4, ORTInterOpThreads: 1,
			AffinityCPUCount: 4, CPUSetEffective: "0-3",
		},
		DerivedContract: DerivedContract{
			RunIntentDigest: digest, Backend: intent.State.Backend, Provider: intent.State.Backend,
			NodeSnapshot: intent.TargetNode,
		},
		RepetitionResults: []Repetition{{
			Repetition: 1, MeasurementStartedAt: "2026-01-01T00:00:01Z",
			MeasurementEndedAt: "2026-01-01T00:00:31Z", LatencyMilliseconds: []float64{1},
		}},
	}
}

func TestValidateRuntimeRejectsCPUExecutionDrift(t *testing.T) {
	intent := validIntent()
	base := validRuntimeResult(intent)
	for _, test := range []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{name: "architecture", mutate: func(result *RuntimeResult) { result.Architecture = "aarch64" }},
		{name: "smaller quota", mutate: func(result *RuntimeResult) {
			result.CPUContract.CgroupCPUMax = "200000 100000"
		}},
		{name: "unlimited quota", mutate: func(result *RuntimeResult) {
			result.CPUContract.CgroupCPUMax = "max 100000"
		}},
		{name: "malformed quota", mutate: func(result *RuntimeResult) {
			result.CPUContract.CgroupCPUMax = "400000"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			if err := ValidateRuntime(intent, "pod-uid", candidate); err == nil {
				t.Fatal("expected CPU execution drift rejection")
			}
		})
	}
}

func TestValidateRuntimeRequiresCompleteRKNNLiteEvidence(t *testing.T) {
	intent := validRKNNIntent(false)
	result := validRuntimeResult(intent)
	result.Architecture = "aarch64"
	result.Provider = ""
	result.AvailableProviders = nil
	result.CPUContract.ORTIntraOpThreads = 0
	result.CPUContract.ORTInterOpThreads = 0
	result.DerivedContract.Provider = ""
	result.RKNN = validRKNNRuntimeEvidence(intent.Measurement.Batch)
	if err := ValidateRuntime(intent, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RuntimeResult){
		func(candidate *RuntimeResult) { candidate.RKNN.InitSucceeded = false },
		func(candidate *RuntimeResult) { candidate.RKNN.SelectedBatch++ },
		func(candidate *RuntimeResult) { candidate.RKNN.CoreMask = "AUTO" },
		func(candidate *RuntimeResult) { candidate.RKNN.Devices[0].Opened = false },
		func(candidate *RuntimeResult) { candidate.Provider = "RKNPUExecutionProvider" },
	} {
		candidate := result
		evidence := *result.RKNN
		evidence.Devices = append([]RKNNDeviceObservation(nil), result.RKNN.Devices...)
		candidate.RKNN = &evidence
		mutate(&candidate)
		if err := ValidateRuntime(intent, "pod-uid", candidate); err == nil {
			t.Fatal("invalid RKNNLite runtime evidence was accepted")
		}
	}
}

func TestValidateRuntimeRetainsV1Alpha1CPUResult(t *testing.T) {
	intent := validIntent()
	result := validRuntimeResult(intent)
	result.SchemaVersion = RuntimeResultSchemaVersionV1Alpha1
	result.Backend = ""
	result.DerivedContract.Backend = ""
	if err := ValidateRuntime(intent, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	result.Backend = intent.State.Backend
	if err := ValidateRuntime(intent, "pod-uid", result); err == nil {
		t.Fatal("retained v1alpha1 runtime result accepted a v1alpha2 backend field")
	}
}

func TestNormalizeRuntimeArchitecture(t *testing.T) {
	for runtime, want := range map[string]string{"x86_64": "amd64", "aarch64": testARM64Architecture} {
		if got := normalizeArchitecture(runtime); got != want {
			t.Fatalf("normalizeArchitecture(%q) = %q, want %q", runtime, got, want)
		}
	}
}

func TestValidateIdleRuntimeRequiresSettlingZeroInferenceAndResidency(t *testing.T) {
	intent := validIntent()
	intent.Measurement = MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, Repetitions: 1,
		WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator,
		OutputValidation: NoOutputValidation,
	}
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	result := IdleRuntimeResult{
		SchemaVersion: IdleRuntimeResultSchemaVersion, Status: "Succeeded", IntentDigest: digest,
		PodUID: "pod-uid", NodeName: intent.TargetNode.Name, Model: intent.State.Model,
		ArtifactDigest: intent.State.ArtifactDigest, Architecture: "x86_64",
		Runtime: intent.State.RuntimeFamily, RuntimeVersion: "1", Backend: intent.State.Backend, Provider: intent.State.Backend,
		AvailableProviders:   []string{intent.State.Backend},
		ModelLoadCompletedAt: "2026-01-01T00:00:00Z", SettlingStartedAt: "2026-01-01T00:00:01Z",
		SettlingCompletedAt: "2026-01-01T00:01:01Z", MeasurementStartedAt: "2026-01-01T00:01:01Z",
		MeasurementEndedAt: "2026-01-01T00:01:31Z", MeasurementElapsedSeconds: 30,
		ModelResidentAtEnd: true,
		CPUContract: RuntimeCPU{CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4,
			ORTInterOpThreads: 1, AffinityCPUCount: 4, CPUSetEffective: "0-3"},
		DerivedContract: DerivedContract{
			RunIntentDigest: digest, Backend: intent.State.Backend, Provider: intent.State.Backend,
			NodeSnapshot: intent.TargetNode,
		},
	}
	if err := ValidateIdleRuntime(intent, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	for _, schemaVersion := range []string{intentSchemaVersionV1Alpha8, intentSchemaVersionV1Alpha7} {
		retainedIntent := intent
		retainedIntent.SchemaVersion = schemaVersion
		retainedDigest, err := retainedIntent.Digest()
		if err != nil {
			t.Fatal(err)
		}
		retainedResult := result
		retainedResult.IntentDigest = retainedDigest
		retainedResult.DerivedContract.RunIntentDigest = retainedDigest
		if err := ValidateIdleRuntime(retainedIntent, "pod-uid", retainedResult); err != nil {
			t.Fatalf("retained %s idle evidence was rejected: %v", schemaVersion, err)
		}
	}
	v1alpha6 := intent
	v1alpha6.SchemaVersion = intentSchemaVersionV1Alpha6
	if err := v1alpha6.Validate(); err == nil || !strings.Contains(err.Error(), "model-loaded idle") {
		t.Fatalf("v1alpha6 accepted model-loaded idle: %v", err)
	}
	for _, mutate := range []func(*IdleRuntimeResult){
		func(r *IdleRuntimeResult) { r.SettlingCompletedAt = "2026-01-01T00:00:30Z" },
		func(r *IdleRuntimeResult) { r.InferenceCountAfterModelLoad = 1 },
		func(r *IdleRuntimeResult) { r.ModelResidentAtEnd = false },
		func(r *IdleRuntimeResult) { r.MeasurementStartedAt = "2026-01-01T00:00:30Z" },
	} {
		candidate := result
		mutate(&candidate)
		if err := ValidateIdleRuntime(intent, "pod-uid", candidate); err == nil {
			t.Fatal("invalid idle runtime evidence was accepted")
		}
	}
}

func TestValidateIdleRuntimeAcceptsModelResidentRKNNLite(t *testing.T) {
	intent := validRKNNIntent(true)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	result := IdleRuntimeResult{
		SchemaVersion: IdleRuntimeResultSchemaVersion, Status: "Succeeded", IntentDigest: digest,
		PodUID: "pod-uid", NodeName: intent.TargetNode.Name, Model: intent.State.Model,
		ArtifactDigest: intent.State.ArtifactDigest, Architecture: "aarch64",
		Runtime: intent.State.RuntimeFamily, RuntimeVersion: "2.3.2", Backend: intent.State.Backend,
		RKNN:                 validRKNNRuntimeEvidence(1),
		ModelLoadCompletedAt: "2026-01-01T00:00:00Z", SettlingStartedAt: "2026-01-01T00:00:01Z",
		SettlingCompletedAt: "2026-01-01T00:01:01Z", MeasurementStartedAt: "2026-01-01T00:01:01Z",
		MeasurementEndedAt: "2026-01-01T00:01:31Z", MeasurementElapsedSeconds: 30,
		ModelResidentAtEnd: true,
		CPUContract:        RuntimeCPU{CgroupCPUMax: "400000 100000", AffinityCPUCount: 4, CPUSetEffective: "0-3"},
		DerivedContract: DerivedContract{
			RunIntentDigest: digest, Backend: intent.State.Backend, NodeSnapshot: intent.TargetNode,
		},
	}
	if err := ValidateIdleRuntime(intent, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	retainedIntent := intent
	retainedIntent.SchemaVersion = intentSchemaVersionV1Alpha8
	retainedDigest, err := retainedIntent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	retainedResult := result
	retainedResult.IntentDigest = retainedDigest
	retainedResult.DerivedContract.RunIntentDigest = retainedDigest
	if err := ValidateIdleRuntime(retainedIntent, "pod-uid", retainedResult); err != nil {
		t.Fatalf("retained v1alpha8 RKNNLite idle evidence was rejected: %v", err)
	}
	result.RKNN.LoadSucceeded = false
	if err := ValidateIdleRuntime(intent, "pod-uid", result); err == nil {
		t.Fatal("idle RKNNLite result without a loaded model was accepted")
	}
}

func validRKNNIntent(idle bool) Intent {
	intent := validIntent()
	intent.Candidate.ExecutionPath = RKNNLiteExecutionPath
	intent.State.Artifact = "model-rk3588-bs1"
	intent.State.ArtifactPath = "/artifact/model.rknn"
	intent.State.RuntimeFamily = RKNNLiteRuntimeFamily
	intent.State.Backend = RKNNLiteBackend
	intent.State.HostContractDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	intent.TargetNode.Architecture = testARM64Architecture
	intent.CPU.ORTIntraOpThreads = 0
	intent.CPU.ORTInterOpThreads = 0
	if idle {
		intent.Measurement = MeasurementContract{
			Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, Repetitions: 1,
			WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator,
			OutputValidation: NoOutputValidation,
		}
	}
	return intent
}

func validRKNNRuntimeEvidence(batch int) *RKNNRuntimeEvidence {
	return &RKNNRuntimeEvidence{
		Package: RKNNPackageObservation{Name: "rknn-toolkit-lite2", Version: "2.3.2"},
		HostLibraries: []RKNNHostLibraryObservation{{
			Name: "librknnrt", Path: "/usr/lib/librknnrt.so",
			Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}},
		Driver: RKNNDriverObservation{Name: "rknpu", VersionPath: "/sys/module/rknpu/version", Version: "0.9.6"},
		Platform: RKNNPlatformObservation{
			CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588", Matched: true,
		},
		Devices: []RKNNDeviceObservation{
			{Path: "/dev/dri/card0", CharacterDevice: true, Opened: true},
			{Path: "/dev/dri/renderD128", CharacterDevice: true, Opened: true},
		},
		CoreMask: RKNNLiteCoreMask, InputLayout: RKNNLiteInputLayout,
		InitSucceeded: true, LoadSucceeded: true, SelectedBatch: batch,
	}
}
