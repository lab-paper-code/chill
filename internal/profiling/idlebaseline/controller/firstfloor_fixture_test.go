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
		Mode: "profiling", ModelInstanceCount: 1, MaxBatchSize: 4, MaxQueueSize: 32,
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

// applyFirstfloorIdleRuntime makes an idle runtime fixture valid firstfloor evidence.
func applyFirstfloorIdleRuntime(config profilingrun.RunConfig, result *profilingrun.IdleRuntimeResult) {
	result.Provider, result.AvailableProviders = fixtureCPUProvider, []string{fixtureCPUProvider}
	result.DerivedContract.Provider = fixtureCPUProvider
	result.CPUContract = fixtureRuntimeCPU(config)
	result.MemoryBytes, result.MemorySource = fixtureMemoryBytes, fixtureMemorySource
	result.Firstfloor = fixtureFirstfloorEvidence(config, result.RuntimeVersion, 0)
}
