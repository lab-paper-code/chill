package profilingrun

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

func tritonRunConfig() RunConfig {
	config := validRunConfig()
	config.State.RuntimeFamily = TritonRuntimeFamily
	config.State.Backend = TritonPythonBackend
	config.State.Triton = &modelcompat.TritonSettings{MaxBatchSize: 4, PriorityLevels: 3, MaxQueueSize: 32}
	return config
}

func TestTritonRunRejectsMissingOrContradictoryNativeSettings(t *testing.T) {
	config := tritonRunConfig()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RunConfig){
		func(c *RunConfig) { c.State.Triton = nil },
		func(c *RunConfig) { c.State.Triton.PriorityLevels = 0 },
		func(c *RunConfig) { c.Measurement.Batch = 5 },
		func(c *RunConfig) { c.State.Backend = cpuExecutionProvider },
		func(c *RunConfig) {
			c.State.RuntimeFamily = cpuORTRuntimeFamily
			c.State.Backend = cpuExecutionProvider
		},
	} {
		config := tritonRunConfig()
		mutate(&config)
		if err := config.Validate(); err == nil {
			t.Fatal("contradictory native condition was accepted")
		}
	}
}

func TestTritonEvidenceRequiresActualBackendAndFixedNativeBatch(t *testing.T) {
	config := tritonRunConfig()
	config.Measurement.Batch = 2
	makeResult := func() RuntimeResult {
		result := validRuntimeResult(config)
		result.BatchSize = 2
		result.OutputValidation.OutputShapes = [][]int{{2, 1000}}
		result.Provider = cpuExecutionProvider
		result.AvailableProviders = []string{cpuExecutionProvider}
		result.DerivedContract.Provider = cpuExecutionProvider
		result.MemoryBytes = 123456
		result.MemorySource = memoryPeakCgroupV2
		result.Triton = &TritonRuntimeEvidence{
			ORTVersion: "1.27.0", ModelInstanceCount: 1, MaxBatchSize: 4,
			PreferredBatchSizes: []int{4},
			PriorityLevels:      3, MaxQueueSize: 32, WorkloadUIDGuard: "same-workload-uid-v1",
			DefaultQueueTimeoutMicroseconds: 9223372036854777, TimeoutAction: "REJECT", AllowTimeoutOverride: true,
			ExecutionCount: 1, InferenceCount: 2, BatchExecutionCounts: map[string]uint64{"2": 1},
		}
		return result
	}
	if err := ValidateRuntime(config, "pod-uid", makeResult()); err != nil {
		t.Fatal(err)
	}
	armConfig := config
	armConfig.TargetNode.Architecture = testARM64Architecture
	armEvidence := makeResult()
	armEvidence.Architecture = runtimeAArch64
	armEvidence.DerivedContract.NodeSnapshot = armConfig.TargetNode
	if err := ValidateRuntime(armConfig, "pod-uid", armEvidence); err != nil {
		t.Fatalf("matching native CPU architecture was rejected: %v", err)
	}
	v1 := makeResult()
	v1.MemorySource = memoryPeakCgroupV1
	if err := ValidateRuntime(config, "pod-uid", v1); err != nil {
		t.Fatalf("exact cgroup-v1 kernel memory peak was rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{"legacy ORT family", func(r *RuntimeResult) { r.Runtime = cpuORTRuntimeFamily }},
		{"backend name is not provider", func(r *RuntimeResult) { r.Provider = "python" }},
		{"missing native evidence", func(r *RuntimeResult) { r.Triton = nil }},
		{"unobserved memory", func(r *RuntimeResult) { r.MemoryBytes = 0 }},
		{"artifact size is not memory", func(r *RuntimeResult) { r.MemorySource = "artifact-size" }},
		{"different configured maximum", func(r *RuntimeResult) { r.Triton.MaxBatchSize = 2 }},
		{"native lifetime override disabled", func(r *RuntimeResult) { r.Triton.AllowTimeoutOverride = false }},
		{"native expiration delays instead of rejecting", func(r *RuntimeResult) { r.Triton.TimeoutAction = "DELAY" }},
		{"different priority lanes", func(r *RuntimeResult) { r.Triton.PriorityLevels = 4 }},
		{"added batch wait", func(r *RuntimeResult) { r.Triton.MaxQueueDelayMicroseconds = 1 }},
		{"K independent requests are not batch K", func(r *RuntimeResult) {
			r.Triton.ExecutionCount = 2
			r.Triton.BatchExecutionCounts = map[string]uint64{"1": 2}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := makeResult()
			test.mutate(&result)
			if err := ValidateRuntime(config, "pod-uid", result); err == nil {
				t.Fatal("invalid native execution evidence was accepted")
			}
		})
	}
}

func TestTritonIdleEvidenceRejectsInferTraffic(t *testing.T) {
	config := tritonRunConfig()
	config.Measurement = MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 1, DurationSeconds: 30,
		WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator, OutputValidation: NoOutputValidation,
	}
	result := IdleRuntimeResult{
		SchemaVersion: IdleRuntimeResultSchemaVersion, Status: "Succeeded", PodUID: "pod-uid",
		NodeName: config.TargetNode.Name, Model: config.State.Model, Architecture: "x86_64",
		Runtime: "triton", RuntimeVersion: "2", Backend: "python", Provider: cpuExecutionProvider,
		AvailableProviders: []string{cpuExecutionProvider}, Inputs: config.Delivery.ArtifactCapability.Tensors,
		ModelLoadCompletedAt: "2026-01-01T00:00:00Z", SettlingStartedAt: "2026-01-01T00:00:01Z",
		SettlingCompletedAt: "2026-01-01T00:00:02Z", MeasurementStartedAt: "2026-01-01T00:00:02Z",
		MeasurementEndedAt: "2026-01-01T00:00:32Z", MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true,
		MemoryBytes: 123456, MemorySource: memoryPeakCgroupV2,
		CPUContract: RuntimeCPU{CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4,
			ORTInterOpThreads: 1, AffinityCPUCount: 4, CPUSetEffective: "0-3"},
		DerivedContract: DerivedContract{Backend: "python", Provider: cpuExecutionProvider, NodeSnapshot: config.TargetNode},
		Triton: &TritonRuntimeEvidence{ORTVersion: "1.27.0", ModelInstanceCount: 1, MaxBatchSize: 4,
			PreferredBatchSizes: []int{4},
			PriorityLevels:      3, MaxQueueSize: 32, WorkloadUIDGuard: "same-workload-uid-v1", BatchExecutionCounts: map[string]uint64{},
			DefaultQueueTimeoutMicroseconds: 9223372036854777, TimeoutAction: "REJECT", AllowTimeoutOverride: true},
	}
	if err := ValidateIdleRuntime(config, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	result.MemorySource = memoryPeakCgroupV1
	if err := ValidateIdleRuntime(config, "pod-uid", result); err != nil {
		t.Fatalf("idle cgroup-v1 kernel memory peak was rejected: %v", err)
	}
	result.Triton.ExecutionCount = 1
	result.Triton.InferenceCount = 1
	result.Triton.BatchExecutionCounts["1"] = 1
	if err := ValidateIdleRuntime(config, "pod-uid", result); err == nil {
		t.Fatal("idle native inference traffic was accepted")
	}
}
