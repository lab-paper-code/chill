package profiler

import "github.com/lab-paper-code/chill/internal/profilingrun"

const (
	CPUORTWorkerPolicy        = "cpu-ort-worker"
	CPUORTWorkerPolicyVersion = "v1"
	CPUORTIdlePolicyVersion   = "idle-v1"

	mebibyte                 = int64(1024 * 1024)
	maxMetricEventsV1        = int64(50_000)
	maxWorkloadEventsV1      = 10_000
	maxMetricResultBytesV1   = 8 * mebibyte
	maxWorkloadResultBytesV1 = 8 * mebibyte
	coordinationReserveV1    = 1 * mebibyte
)

// LegacyFrozenExecutionPolicy reconstructs the exact implementation envelope
// used by retained two- and three-payload intent records. New Runs never use
// this fallback; their configured envelope is frozen verbatim.
func LegacyFrozenExecutionPolicy(intent profilingrun.Intent) (FrozenExecutionPolicy, error) {
	if err := intent.Validate(); err != nil {
		return FrozenExecutionPolicy{}, err
	}
	policyVersion := CPUORTWorkerPolicyVersion
	if intent.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		policyVersion = CPUORTIdlePolicyVersion
	}
	policy := ExecutionPolicy{
		SchemaVersion: ExecutionPolicySchemaVersion,
		ExecutionPolicySpec: ExecutionPolicySpec{
			Name: CPUORTWorkerPolicy, DeviceClass: "*", ExecutionPath: firstExecutionPath,
			WorkloadMode:                  intent.Measurement.WorkloadMode,
			ActiveDeadlineOverheadSeconds: 180, AttemptTimeoutOverheadSeconds: 150,
			ObserverReadyTimeoutSeconds: 30, ObserverStartOverheadSeconds: 120,
			MetricResultOverheadSeconds: 30, WorkloadEndFenceTimeoutSeconds: 15,
			WorkloadResultTimeoutSeconds: 30,
			MaxLatencySamples:            1_000_000, MaxInputBytes: 64 * mebibyte,
			MaxRuntimeResultBytes: 32 * mebibyte, MaxMetricResultBytes: maxMetricResultBytesV1,
			MaxMetricEvents: int(maxMetricEventsV1), MaxWorkloadEvents: maxWorkloadEventsV1,
			MaxWorkloadResultBytes:    maxWorkloadResultBytesV1,
			RuntimeMemoryRequestBytes: 128 * mebibyte, RuntimeMemoryLimitBytes: 1024 * mebibyte,
			ObserverCPURequest: "5m", ObserverCPULimit: "100m",
			ObserverMemoryRequestBytes: 16 * mebibyte, ObserverMemoryLimitBytes: 64 * mebibyte,
			WorkloadObserverCPURequest: "5m", WorkloadObserverCPULimit: "100m",
			WorkloadObserverMemoryRequestBytes: 32 * mebibyte,
			WorkloadObserverMemoryLimitBytes:   128 * mebibyte,
			CoordinationReserveBytes:           coordinationReserveV1, WorkspaceBytes: 128 * mebibyte,
		},
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return FrozenExecutionPolicy{}, err
	}
	frozen := FrozenExecutionPolicy{
		SchemaVersion: FrozenExecutionPolicySchemaVersion,
		Source: ExecutionPolicySource{
			Kind:                 BuiltInExecutionPolicySourceKind,
			Name:                 CPUORTWorkerPolicy + "/" + policyVersion,
			CatalogContentDigest: policyDigest,
		},
		Policy: policy,
	}
	return frozen, frozen.Validate()
}
