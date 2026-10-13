package profilingrun

import (
	"math"
	"testing"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
)

func firstfloorRunConfig() RunConfig {
	config := validRunConfig()
	config.State.RuntimeFamily = FirstfloorRuntimeFamily
	config.State.Backend = FirstfloorORTCPUBackend
	config.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	return config
}

func firstfloorResult(config RunConfig) RuntimeResult {
	result := validRuntimeResult(config)
	result.Provider = cpuExecutionProvider
	result.AvailableProviders = []string{cpuExecutionProvider}
	result.DerivedContract.Provider = cpuExecutionProvider
	result.MemoryBytes, result.MemorySource = 123456, memoryPeakCgroupV2
	result.CPUContract.AffinityCPUs = []int{0, 1, 2, 3}
	inputs := make([]firstfloor.InputSpec, len(result.Inputs))
	for i, tensor := range result.Inputs {
		shape := make([]int, len(tensor.SampleShape))
		for j, dimension := range tensor.SampleShape {
			shape[j] = int(dimension)
		}
		inputs[i] = firstfloor.InputSpec{Name: tensor.Name, Datatype: tensor.Datatype, Shape: shape}
	}
	result.Firstfloor = &FirstfloorRuntimeEvidence{
		Mode: "profiling", ModelInstanceCount: 1, MaxBatchSize: 4, MaxQueueSize: 32,
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy),
		ExecutionCount: 1, InferenceCount: 1, BatchExecutionCounts: map[string]uint64{"1": 1},
		Runtime: firstfloor.RuntimeMetadata{Runtime: FirstfloorORTCPUBackend, RuntimeVersion: result.RuntimeVersion,
			ModelPath: "/model/model.onnx", Inputs: inputs, Providers: []string{cpuExecutionProvider},
			AvailableProviders: []string{cpuExecutionProvider}, Device: "cpu", CPUAffinity: []int{0, 1, 2, 3},
			IntraOpThreads: 4, InterOpThreads: 1, InitializedBatches: []int{1, 2, 4}, CgroupCPUMax: "400000 100000", CPUSet: "0-3"},
	}
	result.RepetitionResults[0].NativeSamples = []NativeServiceSample{{BatchID: 21, BatchSize: 1,
		RunnerServiceMilliseconds: 0.7, BackendCallMilliseconds: 0.4, NativeRequestMilliseconds: 0.9}}
	return result
}

func TestFirstfloorEvidenceBindsSelectedDeadlinePolicy(t *testing.T) {
	for _, policy := range []string{"", "drop", "keep", "defer"} {
		config := firstfloorRunConfig()
		config.State.Firstfloor.DeadlinePolicy = policy
		result := firstfloorResult(config)
		if err := ValidateRuntime(config, "pod-uid", result); err != nil {
			t.Fatalf("policy %q: %v", policy, err)
		}
		result.Firstfloor.DeadlinePolicy = "invalid"
		if err := ValidateRuntime(config, "pod-uid", result); err == nil {
			t.Fatalf("policy %q accepted mismatched actor policy", policy)
		}
	}
}

func TestFirstfloorEvidenceRetainsDistinctCorrelatedSpans(t *testing.T) {
	config := firstfloorRunConfig()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntime(config, "pod-uid", firstfloorResult(config)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{"standalone family", func(r *RuntimeResult) { r.Runtime = cpuORTRuntimeFamily }},
		{"synthetic runner", func(r *RuntimeResult) { r.Firstfloor.Runtime.Synthetic = true }},
		{"missing memory", func(r *RuntimeResult) { r.MemoryBytes = 0 }},
		{"wrong memory scope", func(r *RuntimeResult) { r.MemorySource = "process-rss" }},
		{"different input", func(r *RuntimeResult) { r.Firstfloor.Runtime.Inputs[0].Name = "other" }},
		{"wrong max", func(r *RuntimeResult) { r.Firstfloor.MaxBatchSize = 2 }},
		{"batch not initialized", func(r *RuntimeResult) { r.Firstfloor.Runtime.InitializedBatches = []int{2, 4} }},
		{"wrong provider", func(r *RuntimeResult) { r.Firstfloor.Runtime.Providers[0] = "CUDAExecutionProvider" }},
		{"wrong actual affinity", func(r *RuntimeResult) { r.Firstfloor.Runtime.CPUAffinity = []int{1, 2, 3, 4} }},
		{"uncorrelated samples", func(r *RuntimeResult) { r.RepetitionResults[0].NativeSamples = nil }},
		{"wrong measured items", func(r *RuntimeResult) { r.Firstfloor.InferenceCount = 2 }},
		{"wrong batch", func(r *RuntimeResult) { r.RepetitionResults[0].NativeSamples[0].BatchSize = 2 }},
		{"invalid backend", func(r *RuntimeResult) { r.RepetitionResults[0].NativeSamples[0].BackendCallMilliseconds = math.NaN() }},
		{"nested span order", func(r *RuntimeResult) { r.RepetitionResults[0].NativeSamples[0].RunnerServiceMilliseconds = 0.2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := firstfloorResult(config)
			test.mutate(&result)
			if err := ValidateRuntime(config, "pod-uid", result); err == nil {
				t.Fatal("contradictory native evidence accepted")
			}
		})
	}
}

func TestFirstfloorNativeSamplesRejectReusedBatchIdentity(t *testing.T) {
	config := firstfloorRunConfig()
	repetition := firstfloorResult(config).RepetitionResults[0]
	repetition.LatencyMilliseconds = []float64{1, 1}
	repetition.NativeSamples = append(repetition.NativeSamples, repetition.NativeSamples[0])
	if err := validateNativeSamples(config, []Repetition{repetition}); err == nil {
		t.Fatal("reused batch ID accepted")
	}
}

func TestFirstfloorIdleEvidenceRejectsInferenceCounts(t *testing.T) {
	config := firstfloorRunConfig()
	result := firstfloorResult(config)
	result.Firstfloor.ExecutionCount, result.Firstfloor.InferenceCount = 0, 0
	result.Firstfloor.BatchExecutionCounts = map[string]uint64{}
	if err := validateFirstfloorRuntimeEvidence(config, result.Firstfloor, 0); err != nil {
		t.Fatal(err)
	}
	result.Firstfloor.BatchExecutionCounts["1"] = 1
	if err := validateFirstfloorRuntimeEvidence(config, result.Firstfloor, 0); err == nil {
		t.Fatal("idle inference accepted")
	}
}

func TestFirstfloorCUDARejectsCPUFallbackAndWrongWholeGPU(t *testing.T) {
	config := firstfloorRunConfig()
	config.State.Backend = FirstfloorORTCUDABackend
	config.Delivery.RuntimeAccess = &CandidateReportRuntimeAccess{GPUUUID: "GPU-a", Resources: map[string]string{"nvidia.com/gpu": "1"}}
	result := firstfloorResult(config)
	result.Firstfloor.Runtime.Runtime = FirstfloorORTCUDABackend
	if err := validateFirstfloorPath(config, result.Firstfloor, nil, result.Provider, result.AvailableProviders, result.CPUContract, result.RuntimeVersion); err == nil {
		t.Fatal("CUDA CPU fallback accepted")
	}
	result.Provider = "CUDAExecutionProvider"
	result.AvailableProviders = []string{result.Provider}
	result.Firstfloor.Runtime.Providers = []string{result.Provider}
	result.Firstfloor.Runtime.AvailableProviders = []string{result.Provider}
	result.Firstfloor.Runtime.Device, result.Firstfloor.Runtime.GPUUUID = "cuda", "GPU-b"
	if err := validateFirstfloorPath(config, result.Firstfloor, nil, result.Provider, result.AvailableProviders, result.CPUContract, result.RuntimeVersion); err == nil {
		t.Fatal("different whole GPU accepted")
	}
	result.Firstfloor.Runtime.GPUUUID = "GPU-a"
	if err := validateFirstfloorPath(config, result.Firstfloor, nil, result.Provider, result.AvailableProviders, result.CPUContract, result.RuntimeVersion); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneEvidenceCannotAssertActorSpans(t *testing.T) {
	config := validRunConfig()
	result := validRuntimeResult(config)
	result.RepetitionResults[0].NativeSamples = firstfloorResult(firstfloorRunConfig()).RepetitionResults[0].NativeSamples
	if err := ValidateRuntime(config, "pod-uid", result); err == nil {
		t.Fatal("reference result asserted native actor execution")
	}
}

func TestFirstfloorEvidenceBindsRequestedCPUIdsNotOnlyQuota(t *testing.T) {
	config := firstfloorRunConfig()
	config.CPU.AffinityCPUs = []int{0, 1, 2, 3}
	result := firstfloorResult(config)
	if err := ValidateRuntime(config, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	idleNative := *result.Firstfloor
	idleNative.ExecutionCount, idleNative.InferenceCount = 0, 0
	idleNative.BatchExecutionCounts = map[string]uint64{}
	idle := IdleRuntimeResult{CPUContract: result.CPUContract, Firstfloor: &idleNative,
		MemoryBytes: result.MemoryBytes, MemorySource: result.MemorySource, Provider: result.Provider,
		AvailableProviders: result.AvailableProviders, DerivedContract: result.DerivedContract, RuntimeVersion: result.RuntimeVersion}
	if err := validateIdleRuntimePathEvidence(config, idle); err != nil {
		t.Fatal(err)
	}
	// Quota, threads and metadata remain internally consistent, but the
	// actual mask no longer matches the selected immutable placement.
	result.CPUContract.AffinityCPUs = []int{0, 1, 2, 4}
	result.Firstfloor.Runtime.CPUAffinity = []int{0, 1, 2, 4}
	if err := ValidateRuntime(config, "pod-uid", result); err == nil {
		t.Fatal("requested CPU placement silently changed")
	}
	idle.CPUContract.AffinityCPUs = []int{0, 1, 2, 4}
	idle.Firstfloor.Runtime.CPUAffinity = []int{0, 1, 2, 4}
	if err := validateIdleRuntimePathEvidence(config, idle); err == nil {
		t.Fatal("idle ignored frozen CPU placement")
	}
}
