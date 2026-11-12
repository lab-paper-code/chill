package profilingrun

import (
	"testing"
	"time"
)

func TestValidateRuntimeRejectsMaterialDrift(t *testing.T) {
	config := firstfloorRunConfig()
	valid := firstfloorResult(config)
	if err := ValidateRuntime(config, "pod-uid", valid); err != nil {
		t.Fatal(err)
	}
	invalidSchema := valid
	invalidSchema.SchemaVersion = "chill.dacs.io/batch-characterization.v1alpha9"
	if err := ValidateRuntime(config, "pod-uid", invalidSchema); err == nil {
		t.Fatal("old active runtime schema accepted")
	}
	invalid := valid
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
		!ended.Equal(time.Date(2026, time.January, 1, 0, 1, 31, 0, time.UTC)) {
		t.Fatalf("measurement window = %s..%s", started, ended)
	}
}

func TestActiveDurationToleranceCannotShortenMinimumWindow(t *testing.T) {
	config := firstfloorRunConfig()
	result := firstfloorResult(config)
	result.Phases[0].MeasurementElapsedSeconds = 29.6
	config.Measurement.DurationToleranceSeconds = .5
	if err := ValidateRuntime(config, "pod-uid", result); err == nil {
		t.Fatal("duration tolerance shortened the required elapsed window")
	}
	result.Phases[0].MeasurementElapsedSeconds = 30
	if err := ValidateRuntime(config, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeEvidenceRejectsRawInputFieldDrift(t *testing.T) {
	for name, inputs := range map[string][]string{"renamed": {"other"}, "missing": nil, "extra": {"image", "question"}} {
		t.Run(name, func(t *testing.T) {
			config := firstfloorRunConfig()
			result := firstfloorResult(config)
			result.Inputs = inputs
			if err := ValidateRuntime(config, "pod-uid", result); err == nil {
				t.Fatal("runtime input field evidence drift was accepted")
			}
		})
	}
}

func TestValidateRuntimeRejectsInvalidPhaseEvidence(t *testing.T) {
	config := firstfloorRunConfig()
	base := firstfloorResult(config)

	for _, test := range []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{name: "wrong sequence", mutate: func(result *RuntimeResult) {
			result.Phases[0].Kind = "saturation"
		}},
		{name: "window outside measurement", mutate: func(result *RuntimeResult) {
			result.Phases[0].MeasurementStartedAt = "2025-12-31T23:59:59Z"
		}},
		{name: "zero latency", mutate: func(result *RuntimeResult) {
			result.Phases[0].LatencyMilliseconds[0] = 0
		}},
		{name: "target duration drift", mutate: func(result *RuntimeResult) {
			other := 10.0
			result.TargetDurationSeconds = &other
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Phases = append([]Phase(nil), base.Phases...)
			candidate.Phases[0].LatencyMilliseconds = append(
				[]float64(nil), base.Phases[0].LatencyMilliseconds...,
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
	count := config.Measurement.MinServiceSamples + 1
	return RuntimeResult{
		SchemaVersion: RuntimeResultSchemaVersion, Status: "Succeeded",
		ExperimentID: "pod-uid", NodeName: config.TargetNode.Name, Model: config.State.Model,
		Runtime:      config.State.RuntimeFamily,
		Architecture: "x86_64", RuntimeVersion: "1", Backend: config.State.Backend, Provider: config.State.Backend,
		AvailableProviders: []string{config.State.Backend},
		Inputs:             config.State.Preprocessor.Inputs,
		BatchSize:          1, WarmupIterations: 20, WarmupCompletedAt: "2026-01-01T00:00:00Z",
		InferenceCount: count, MeasurementMode: "duration",
		MeasurementStartedAt: "2026-01-01T00:00:01Z", MeasurementEndedAt: "2026-01-01T00:00:31Z",
		MeasurementElapsedSeconds: 30, TargetDurationSeconds: &targetDuration,
		CPUContract: RuntimeCPU{
			CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4, ORTInterOpThreads: 1,
			AffinityCPUCount: 4, CPUSetEffective: "0-3",
		},
		DerivedContract: DerivedContract{
			Backend: config.State.Backend, Provider: config.State.Backend,
			NodeSnapshot: config.TargetNode,
		},
	}
}

func TestValidateRuntimeRejectsCPUExecutionDrift(t *testing.T) {
	config := firstfloorRunConfig()
	base := firstfloorResult(config)
	for _, test := range []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{name: "architecture", mutate: func(result *RuntimeResult) { result.Architecture = "aarch64" }},
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
	config := firstfloorFrozenConfig(FirstfloorRKNNLiteBackend)
	config.TargetNode.Architecture = testARM64Architecture
	config.Measurement.Batch = 1
	config.Delivery = rknnDeliveryForTest(1, config.State.HostContract)
	result := firstfloorResult(config)
	result.Architecture = "aarch64"
	result.RuntimeVersion = "2.3.2"
	result.Provider = ""
	result.AvailableProviders = nil
	result.CPUContract.ORTIntraOpThreads = 0
	result.CPUContract.ORTInterOpThreads = 0
	result.DerivedContract.Provider = ""
	result.RKNN = validRKNNRuntimeEvidence(config.Measurement.Batch)
	native := &result.Firstfloor.Runtime
	native.Runtime, native.RuntimeVersion, native.ModelFiles = FirstfloorRKNNLiteBackend, "2.3.2", config.State.ModelFiles()
	native.Providers, native.AvailableProviders, native.Device = nil, nil, "npu"
	native.IntraOpThreads, native.InterOpThreads = 0, 0
	const observedVersion = "2.3.1"
	result.RuntimeVersion, native.RuntimeVersion = observedVersion, observedVersion
	result.RKNN.Package.Version = ""
	result.RKNN.Driver.Version = ""
	result.RKNN.Platform.Matched = false
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

func TestVerifiedRuntimeAcceptsDeclaredLabelsAndDiagnosticRounding(t *testing.T) {
	config := firstfloorRunConfig()
	config.Delivery.RuntimePackages = []CandidateReportRuntimePackage{
		{Name: "onnxruntime", Version: "declared-version"}, {Name: "numpy", Version: "declared-numpy"},
	}
	result := firstfloorResult(config)
	result.DerivedContract.NodeSnapshot.UID = "historical-node"
	result.DerivedContract.Backend = "historical-backend"
	result.DerivedContract.Provider = "historical-provider"
	result.Firstfloor.Runtime.NumpyVersion = "observed-numpy"
	result.Phases[0].NativeSamples[0].RunnerServiceMilliseconds += 1e-12
	result.RuntimeVersion = ""
	result.Firstfloor.Runtime.RuntimeVersion = "observed-label"
	verified, err := ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRuntimeResult(verified, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	result.Phases[0].NativeSamples[0].ServiceDurationNanos = 0
	if _, err := VerifyRuntimeResult(verified, "pod-uid", result); err == nil {
		t.Fatal("missing native duration was accepted")
	}
}

func TestValidateIdleRuntimeRequiresSettlingZeroInferenceAndResidency(t *testing.T) {
	config := firstfloorRunConfig()
	active := firstfloorResult(config)
	config.LoadOriginNode, config.LoadOriginGatewayPod = "", nil
	config.Measurement = MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: IdleModelLoadedWorkloadMode,
	}
	native := *active.Firstfloor
	native.ExecutionCount, native.InferenceCount = 0, 0
	native.BatchExecutionCounts = map[string]uint64{}
	result := IdleRuntimeResult{
		SchemaVersion: IdleRuntimeResultSchemaVersion, Status: "Succeeded",
		PodUID: "pod-uid", NodeName: config.TargetNode.Name, Model: config.State.Model,
		Architecture: "x86_64",
		Runtime:      config.State.RuntimeFamily, RuntimeVersion: active.RuntimeVersion, Backend: config.State.Backend,
		Provider: active.Provider, AvailableProviders: active.AvailableProviders,
		Inputs:               config.State.Preprocessor.Inputs,
		ModelLoadCompletedAt: "2026-01-01T00:00:00Z", SettlingStartedAt: "2026-01-01T00:00:01Z",
		SettlingCompletedAt: "2026-01-01T00:01:01Z", MeasurementStartedAt: "2026-01-01T00:01:01Z",
		MeasurementEndedAt: "2026-01-01T00:01:31Z", MeasurementElapsedSeconds: 30,
		ModelResidentAtEnd: true, CPUContract: active.CPUContract, DerivedContract: active.DerivedContract,
		MemoryBytes: active.MemoryBytes, MemorySource: active.MemorySource, Firstfloor: &native,
	}
	result.DerivedContract.NodeSnapshot.UID = "historical-node"
	result.DerivedContract.Backend = "historical-backend"
	result.DerivedContract.Provider = "historical-provider"
	result.RuntimeVersion = ""
	result.Firstfloor.Runtime.RuntimeVersion = ""
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
