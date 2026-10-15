package projection

import (
	"strconv"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const fixtureCPUProvider = "CPUExecutionProvider"

// applyFirstfloorRuntime makes an active runtime fixture valid firstfloor evidence.
func applyFirstfloorRuntime(config profilingrun.RunConfig, result *profilingrun.RuntimeResult) {
	result.Provider, result.AvailableProviders = fixtureCPUProvider, []string{fixtureCPUProvider}
	result.DerivedContract.Provider = fixtureCPUProvider
	stat := result.CPUContract.CgroupCPUStatDelta
	result.CPUContract = profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: config.CPU.ORTIntraOpThreads,
		ORTInterOpThreads: config.CPU.ORTInterOpThreads, AffinityCPUCount: 4,
		AffinityCPUs: []int{0, 1, 2, 3}, CPUSetEffective: "0-3", CgroupCPUStatDelta: stat,
	}
	result.MemoryBytes, result.MemorySource = 123456, "cgroup-v2-memory.peak:runtime-container-lifetime"
	executions := 0
	for i := range result.RepetitionResults {
		repetition := &result.RepetitionResults[i]
		repetition.NativeSamples = make([]profilingrun.NativeServiceSample, len(repetition.LatencyMilliseconds))
		for j := range repetition.NativeSamples {
			executions++
			repetition.NativeSamples[j] = profilingrun.NativeServiceSample{
				BatchID: uint64(executions), BatchSize: config.Measurement.Batch, RunnerServiceMilliseconds: 0.7,
				BackendCallMilliseconds: 0.4, NativeRequestMilliseconds: 0.9,
			}
		}
	}
	tensors := config.Delivery.ArtifactCapability.Tensors
	inputs := make([]firstfloor.InputSpec, len(tensors))
	for i, tensor := range tensors {
		shape := make([]int, len(tensor.SampleShape))
		for j, dimension := range tensor.SampleShape {
			shape[j] = int(dimension)
		}
		inputs[i] = firstfloor.InputSpec{Name: tensor.Name, Datatype: tensor.Datatype, Shape: shape}
	}
	cpu := result.CPUContract
	result.Firstfloor = &profilingrun.FirstfloorRuntimeEvidence{
		Mode: "profiling", ModelInstanceCount: 1, MaxBatchSize: 4, MaxQueueSize: 32,
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy),
		ExecutionCount: uint64(executions), InferenceCount: uint64(executions * config.Measurement.Batch),
		BatchExecutionCounts: map[string]uint64{strconv.Itoa(config.Measurement.Batch): uint64(executions)},
		Runtime: firstfloor.RuntimeMetadata{
			Runtime: config.State.Backend, RuntimeVersion: result.RuntimeVersion, ModelPath: "/model/model.onnx",
			Inputs: inputs, Providers: []string{fixtureCPUProvider}, AvailableProviders: []string{fixtureCPUProvider},
			Device: "cpu", CPUAffinity: cpu.AffinityCPUs, IntraOpThreads: cpu.ORTIntraOpThreads,
			InterOpThreads: cpu.ORTInterOpThreads, InitializedBatches: []int{config.Measurement.Batch},
			CgroupCPUMax: cpu.CgroupCPUMax, CPUSet: cpu.CPUSetEffective,
		},
	}
}
