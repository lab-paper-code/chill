package profilingrun

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

func TestValidateRuntimeRejectsMaterialAndOutputDrift(t *testing.T) {
	config := validRunConfig()
	valid := validRuntimeResult(config)
	if err := ValidateRuntime(config, "pod-uid", valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.OutputValidation.Passed = false
	if err := ValidateRuntime(config, "pod-uid", invalid); err == nil {
		t.Fatal("expected output drift rejection")
	}
	invalid = valid
	invalid.NodeName = "other-node"
	if err := ValidateRuntime(config, "pod-uid", invalid); err == nil {
		t.Fatal("expected Node drift rejection")
	}
	invalid = valid
	invalid.MeasurementEndedAt = invalid.MeasurementStartedAt
	if err := ValidateRuntime(config, "pod-uid", invalid); err == nil {
		t.Fatal("expected empty measurement window rejection")
	}
	invalid = valid
	invalid.MeasurementEndedAt = "not-a-time"
	if err := ValidateRuntime(config, "pod-uid", invalid); err == nil {
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

func TestRuntimeEvidenceRejectsNamedInputDrift(t *testing.T) {
	for _, field := range []string{"name", testDatatypeField, testShapeField, "missing"} {
		t.Run(field, func(t *testing.T) {
			config := validRunConfig()
			result := validRuntimeResult(config)
			switch field {
			case "name":
				result.Inputs[0].Name = testDifferentIdentity
			case testDatatypeField:
				result.Inputs[0].Datatype = testINT64Datatype
			case testShapeField:
				result.Inputs[0].SampleShape[0]++
			case "missing":
				result.Inputs = nil
			}
			if err := ValidateRuntime(config, "pod-uid", result); err == nil {
				t.Fatal("runtime input evidence drift was accepted")
			}
		})
	}
}

func TestValidateRuntimeRejectsInvalidRepetitionEvidence(t *testing.T) {
	config := validRunConfig()
	base := validRuntimeResult(config)

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
			if err := ValidateRuntime(config, "pod-uid", candidate); err == nil {
				t.Fatal("expected invalid repetition evidence rejection")
			}
		})
	}
}

func validRuntimeResult(config RunConfig) RuntimeResult {
	targetDuration := float64(config.Measurement.DurationSeconds)
	return RuntimeResult{
		SchemaVersion: RuntimeResultSchemaVersion, Status: "Succeeded",
		ExperimentID: "pod-uid", NodeName: config.TargetNode.Name, Model: config.State.Model,
		Runtime:      config.State.RuntimeFamily,
		Architecture: "x86_64", RuntimeVersion: "1", Backend: config.State.Backend, Provider: config.State.Backend,
		AvailableProviders: []string{config.State.Backend},
		Inputs:             modelcompat.CloneInputTensors(config.Delivery.ArtifactCapability.Tensors),
		BatchSize:          1, WarmupIterations: 20, WarmupCompletedAt: "2026-01-01T00:00:00Z",
		InferenceCount: 1, MeasurementMode: "duration",
		MeasurementStartedAt: "2026-01-01T00:00:01Z", MeasurementEndedAt: "2026-01-01T00:00:31Z",
		MeasurementElapsedSeconds: 30, TargetDurationSeconds: &targetDuration,
		OutputValidation: OutputValidation{
			Method: config.Measurement.OutputValidation, Passed: true, OutputShapes: [][]int{{1, 1000}},
		},
		CPUContract: RuntimeCPU{
			CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4, ORTInterOpThreads: 1,
			AffinityCPUCount: 4, CPUSetEffective: "0-3",
		},
		DerivedContract: DerivedContract{
			Backend: config.State.Backend, Provider: config.State.Backend,
			NodeSnapshot: config.TargetNode,
		},
		RepetitionResults: []Repetition{{
			Repetition: 1, MeasurementStartedAt: "2026-01-01T00:00:01Z",
			MeasurementEndedAt: "2026-01-01T00:00:31Z", LatencyMilliseconds: []float64{1},
		}},
	}
}

func TestValidateRuntimeRejectsCPUExecutionDrift(t *testing.T) {
	config := validRunConfig()
	base := validRuntimeResult(config)
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
			if err := ValidateRuntime(config, "pod-uid", candidate); err == nil {
				t.Fatal("expected CPU execution drift rejection")
			}
		})
	}
}

func TestValidateRuntimeRequiresCompleteRKNNLiteEvidence(t *testing.T) {
	config := validRKNNRunConfig(false)
	result := validRuntimeResult(config)
	result.Architecture = "aarch64"
	result.Provider = ""
	result.AvailableProviders = nil
	result.CPUContract.ORTIntraOpThreads = 0
	result.CPUContract.ORTInterOpThreads = 0
	result.DerivedContract.Provider = ""
	result.RKNN = validRKNNRuntimeEvidence(config.Measurement.Batch)
	if err := ValidateRuntime(config, "pod-uid", result); err != nil {
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
		if err := ValidateRuntime(config, "pod-uid", candidate); err == nil {
			t.Fatal("invalid RKNNLite runtime evidence was accepted")
		}
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
	config := validRunConfig()
	config.Measurement = MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator,
		OutputValidation: NoOutputValidation,
	}
	result := IdleRuntimeResult{
		SchemaVersion: IdleRuntimeResultSchemaVersion, Status: "Succeeded",
		PodUID: "pod-uid", NodeName: config.TargetNode.Name, Model: config.State.Model,
		Architecture: "x86_64",
		Runtime:      config.State.RuntimeFamily, RuntimeVersion: "1", Backend: config.State.Backend, Provider: config.State.Backend,
		AvailableProviders:   []string{config.State.Backend},
		Inputs:               modelcompat.CloneInputTensors(config.Delivery.ArtifactCapability.Tensors),
		ModelLoadCompletedAt: "2026-01-01T00:00:00Z", SettlingStartedAt: "2026-01-01T00:00:01Z",
		SettlingCompletedAt: "2026-01-01T00:01:01Z", MeasurementStartedAt: "2026-01-01T00:01:01Z",
		MeasurementEndedAt: "2026-01-01T00:01:31Z", MeasurementElapsedSeconds: 30,
		ModelResidentAtEnd: true,
		CPUContract: RuntimeCPU{CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4,
			ORTInterOpThreads: 1, AffinityCPUCount: 4, CPUSetEffective: "0-3"},
		DerivedContract: DerivedContract{
			Backend: config.State.Backend, Provider: config.State.Backend,
			NodeSnapshot: config.TargetNode,
		},
	}
	if err := ValidateIdleRuntime(config, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*IdleRuntimeResult){
		func(r *IdleRuntimeResult) { r.SettlingCompletedAt = "2026-01-01T00:00:30Z" },
		func(r *IdleRuntimeResult) { r.InferenceCountAfterModelLoad = 1 },
		func(r *IdleRuntimeResult) { r.ModelResidentAtEnd = false },
		func(r *IdleRuntimeResult) { r.MeasurementStartedAt = "2026-01-01T00:00:30Z" },
	} {
		candidate := result
		mutate(&candidate)
		if err := ValidateIdleRuntime(config, "pod-uid", candidate); err == nil {
			t.Fatal("invalid idle runtime evidence was accepted")
		}
	}
}

func TestValidateIdleRuntimeAcceptsModelResidentRKNNLite(t *testing.T) {
	config := validRKNNRunConfig(true)
	result := IdleRuntimeResult{
		SchemaVersion: IdleRuntimeResultSchemaVersion, Status: "Succeeded",
		PodUID: "pod-uid", NodeName: config.TargetNode.Name, Model: config.State.Model,
		Architecture: "aarch64",
		Runtime:      config.State.RuntimeFamily, RuntimeVersion: "2.3.2", Backend: config.State.Backend,
		RKNN:                 validRKNNRuntimeEvidence(1),
		Inputs:               modelcompat.CloneInputTensors(config.Delivery.ArtifactCapability.Tensors),
		ModelLoadCompletedAt: "2026-01-01T00:00:00Z", SettlingStartedAt: "2026-01-01T00:00:01Z",
		SettlingCompletedAt: "2026-01-01T00:01:01Z", MeasurementStartedAt: "2026-01-01T00:01:01Z",
		MeasurementEndedAt: "2026-01-01T00:01:31Z", MeasurementElapsedSeconds: 30,
		ModelResidentAtEnd: true,
		CPUContract:        RuntimeCPU{CgroupCPUMax: "400000 100000", AffinityCPUCount: 4, CPUSetEffective: "0-3"},
		DerivedContract: DerivedContract{
			Backend: config.State.Backend, NodeSnapshot: config.TargetNode,
		},
	}
	if err := ValidateIdleRuntime(config, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	result.RKNN.LoadSucceeded = false
	if err := ValidateIdleRuntime(config, "pod-uid", result); err == nil {
		t.Fatal("idle RKNNLite result without a loaded model was accepted")
	}
}

func validRKNNRunConfig(idle bool) RunConfig {
	config := validRunConfig()
	config.Candidate.ExecutionPath = RKNNLiteExecutionPath
	config.State.Artifact = "model-rk3588-bs1"
	config.State.ArtifactPath = "/artifact/model.rknn"
	config.State.RuntimeFamily = RKNNLiteRuntimeFamily
	config.State.Backend = RKNNLiteBackend
	contract := rknnHostContractForTest()
	config.State.HostContract = &contract
	config.TargetNode.Architecture = testARM64Architecture
	config.CPU.ORTIntraOpThreads = 0
	config.CPU.ORTInterOpThreads = 0
	if idle {
		config.Measurement = MeasurementContract{
			Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
			WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator,
			OutputValidation: NoOutputValidation,
		}
	}
	return config
}

func validRKNNRuntimeEvidence(batch int) *RKNNRuntimeEvidence {
	return &RKNNRuntimeEvidence{
		Package: RKNNPackageObservation{Name: "rknn-toolkit-lite2", Version: "2.3.2"},
		HostLibraries: []RKNNHostLibraryObservation{{
			Name: "librknnrt", Path: "/usr/lib/librknnrt.so",
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
