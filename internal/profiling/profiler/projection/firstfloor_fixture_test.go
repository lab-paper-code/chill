package projection

import (
	"strconv"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/test/fixtures"
)

const fixtureCPUProvider = "CPUExecutionProvider"

// applyFirstfloorRuntime makes an active runtime fixture valid firstfloor evidence.
func applyFirstfloorRuntime(config profilingrun.RunConfig, result *profilingrun.RuntimeResult) {
	result.Provider, result.AvailableProviders = fixtureCPUProvider, []string{fixtureCPUProvider}
	result.DerivedContract.Provider = fixtureCPUProvider
	stat := result.CPUContract.CgroupCPUStatDelta
	result.CPUContract = profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 0,
		ORTInterOpThreads: 0, AffinityCPUCount: 4,
		AffinityCPUs: []int{0, 1, 2, 3}, CPUSetEffective: "0-3", CgroupCPUStatDelta: stat,
	}
	result.MemoryBytes, result.MemorySource = 123456, "cgroup-v2-memory.peak:runtime-container-lifetime"
	started, err := time.Parse(time.RFC3339Nano, result.MeasurementStartedAt)
	if err != nil {
		panic(err)
	}
	points := config.Measurement.OperatingPoints
	workers, sizing := fixtures.PhaseSupply(config.Measurement.Batch, config.Actor.MaxQueueRequests, points.QueueBytesCap,
		points.FramedDecodedRequestUpperBytes, float64(points.RequestLifetimeSeconds), float64(points.DeliveryMarginMilliseconds))
	fixtures.ApplyActivePhases(result, fixtures.PhaseOptions{
		StartedAt: started, Duration: time.Duration(points.PhaseDurationSeconds) * time.Second,
		Batch: config.Measurement.Batch, Samples: config.Measurement.MinServiceSamples + 1, Workers: workers,
		Utilizations: points.Utilizations, SparseMeanGapMilliseconds: points.SparseMeanGapMilliseconds,
		PodUID: result.ExperimentID, Latencies: result.Phases[0].LatencyMilliseconds,
		SupplySizing: sizing,
	})
	executions := result.InferenceCount
	cpu := result.CPUContract
	result.Firstfloor = &profilingrun.FirstfloorRuntimeEvidence{
		Mode: "profiling", ModelInstanceCount: 1, MaxBatchSize: 4, MaxQueueSize: 32,
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy),
		ExecutionCount: uint64(executions), InferenceCount: uint64(executions * config.Measurement.Batch),
		BatchExecutionCounts: map[string]uint64{strconv.Itoa(config.Measurement.Batch): uint64(executions)},
		Runtime: firstfloor.RuntimeMetadata{
			Runtime: config.State.Backend, RuntimeVersion: result.RuntimeVersion, ModelFiles: config.State.ModelFiles(),
			Inputs: config.State.Preprocessor.Inputs, Outputs: config.State.Preprocessor.Outputs, Providers: []string{fixtureCPUProvider}, AvailableProviders: []string{fixtureCPUProvider},
			Device: "cpu", CPUAffinity: cpu.AffinityCPUs, IntraOpThreads: cpu.ORTIntraOpThreads,
			InterOpThreads: cpu.ORTInterOpThreads, InitializedBatches: []int{config.Measurement.Batch},
			CgroupCPUMax: cpu.CgroupCPUMax, CPUSet: cpu.CPUSetEffective,
		},
	}
}
