package controller

import (
	"strconv"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const (
	fixtureCPUProvider  = "CPUExecutionProvider"
	fixtureMemoryBytes  = 123456
	fixtureMemorySource = "cgroup-v2-memory.peak:runtime-container-lifetime"
)

func fixtureRuntimeCPU(config profilingrun.RunConfig) profilingrun.RuntimeCPU {
	return profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: config.CPU.ORTIntraOpThreads,
		ORTInterOpThreads: config.CPU.ORTInterOpThreads, AffinityCPUCount: 4,
		AffinityCPUs: []int{0, 1, 2, 3}, CPUSetEffective: "0-3",
	}
}

func fixtureFirstfloorEvidence(
	config profilingrun.RunConfig, runtimeVersion string, executions uint64,
) *profilingrun.FirstfloorRuntimeEvidence {
	counts := map[string]uint64{}
	if executions > 0 {
		counts[strconv.Itoa(config.Measurement.Batch)] = executions
	}
	cpu := fixtureRuntimeCPU(config)
	return &profilingrun.FirstfloorRuntimeEvidence{
		Mode: "profiling", ModelInstanceCount: 1, MaxBatchSize: int(config.State.Firstfloor.MaxBatchSize), MaxQueueSize: 32,
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy),
		ExecutionCount: executions, InferenceCount: executions * uint64(config.Measurement.Batch),
		BatchExecutionCounts: counts,
		Runtime: firstfloor.RuntimeMetadata{
			Runtime: config.State.Backend, RuntimeVersion: runtimeVersion, ModelFiles: config.State.ModelFiles(),
			Inputs: config.State.Preprocessor.Inputs, Outputs: config.State.Preprocessor.Outputs, Providers: []string{fixtureCPUProvider}, AvailableProviders: []string{fixtureCPUProvider},
			Device: "cpu", CPUAffinity: cpu.AffinityCPUs, IntraOpThreads: cpu.ORTIntraOpThreads,
			InterOpThreads: cpu.ORTInterOpThreads, InitializedBatches: []int{config.Measurement.Batch},
			CgroupCPUMax: cpu.CgroupCPUMax, CPUSet: cpu.CPUSetEffective,
		},
	}
}

// applyFirstfloorRuntime makes an active runtime fixture valid firstfloor evidence.
func applyFirstfloorRuntime(config profilingrun.RunConfig, result *profilingrun.RuntimeResult) {
	result.Provider, result.AvailableProviders = fixtureCPUProvider, []string{fixtureCPUProvider}
	result.DerivedContract.Provider = fixtureCPUProvider
	stat := result.CPUContract.CgroupCPUStatDelta
	result.CPUContract = fixtureRuntimeCPU(config)
	result.CPUContract.CgroupCPUStatDelta = stat
	result.MemoryBytes, result.MemorySource = fixtureMemoryBytes, fixtureMemorySource
	executions := 0
	recordIDs, units := make([]string, config.Measurement.Batch), make([]int, config.Measurement.Batch)
	for k := range recordIDs {
		recordIDs[k], units[k] = "record-"+strconv.Itoa(k), 1
	}
	for i := range result.RepetitionResults {
		repetition := &result.RepetitionResults[i]
		repetition.NativeSamples = make([]profilingrun.NativeServiceSample, len(repetition.LatencyMilliseconds))
		for j := range repetition.NativeSamples {
			executions++
			repetition.NativeSamples[j] = profilingrun.NativeServiceSample{
				BatchID: uint64(executions), BatchSize: config.Measurement.Batch, RecordIDs: recordIDs, Units: units,
				RunnerServiceMilliseconds: 0.7, PreprocessMilliseconds: 0.1, PostprocessMilliseconds: 0.1,
				BackendCallMilliseconds: 0.4, NativeRequestMilliseconds: 0.9,
			}
		}
	}
	result.Firstfloor = fixtureFirstfloorEvidence(config, result.RuntimeVersion, uint64(executions))
}

// applyFirstfloorIdleRuntime makes an idle runtime fixture valid firstfloor evidence.
func applyFirstfloorIdleRuntime(config profilingrun.RunConfig, result *profilingrun.IdleRuntimeResult) {
	result.Provider, result.AvailableProviders = fixtureCPUProvider, []string{fixtureCPUProvider}
	result.DerivedContract.Provider = fixtureCPUProvider
	result.CPUContract = fixtureRuntimeCPU(config)
	result.MemoryBytes, result.MemorySource = fixtureMemoryBytes, fixtureMemorySource
	result.Firstfloor = fixtureFirstfloorEvidence(config, result.RuntimeVersion, 0)
}
