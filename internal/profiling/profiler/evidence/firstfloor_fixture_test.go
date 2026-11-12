package evidence

import (
	"strconv"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/test/fixtures"
)

const (
	fixtureCPUProvider  = "CPUExecutionProvider"
	fixtureMemoryBytes  = 123456
	fixtureMemorySource = "cgroup-v2-memory.peak:runtime-container-lifetime"
)

func fixtureRuntimeCPU() profilingrun.RuntimeCPU {
	return profilingrun.RuntimeCPU{
		CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 0,
		ORTInterOpThreads: 0, AffinityCPUCount: 4,
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
	cpu := fixtureRuntimeCPU()
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

// applyFirstfloorRuntime makes an active runtime fixture valid firstfloor evidence.
func applyFirstfloorRuntime(config profilingrun.RunConfig, result *profilingrun.RuntimeResult) {
	result.Provider, result.AvailableProviders = fixtureCPUProvider, []string{fixtureCPUProvider}
	result.DerivedContract.Provider = fixtureCPUProvider
	stat := result.CPUContract.CgroupCPUStatDelta
	result.CPUContract = fixtureRuntimeCPU()
	result.CPUContract.CgroupCPUStatDelta = stat
	result.MemoryBytes, result.MemorySource = fixtureMemoryBytes, fixtureMemorySource
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
	result.Firstfloor = fixtureFirstfloorEvidence(config, result.RuntimeVersion, uint64(executions))
}

// applyFirstfloorIdleRuntime makes an idle runtime fixture valid firstfloor evidence.
func applyFirstfloorIdleRuntime(config profilingrun.RunConfig, result *profilingrun.IdleRuntimeResult) {
	result.Provider, result.AvailableProviders = fixtureCPUProvider, []string{fixtureCPUProvider}
	result.DerivedContract.Provider = fixtureCPUProvider
	result.CPUContract = fixtureRuntimeCPU()
	result.MemoryBytes, result.MemorySource = fixtureMemoryBytes, fixtureMemorySource
	result.Firstfloor = fixtureFirstfloorEvidence(config, result.RuntimeVersion, 0)
}
