package profilingrun

// Terminal resolution reasons a ProfilingRun and its campaign record when the
// same request can never resolve.
const (
	// ResolutionReasonInvalidRunSpec marks an immutable Run request that cannot be projected.
	ResolutionReasonInvalidRunSpec = "InvalidRunSpec"
	// ResolutionReasonExecutionPolicyInvalid marks an invalid selected execution policy.
	ResolutionReasonExecutionPolicyInvalid = "ExecutionPolicyInvalid"
	// ResolutionReasonActuationPolicyInvalid marks an invalid selected actuation policy.
	ResolutionReasonActuationPolicyInvalid = "ActuationPolicyInvalid"
)
