package profiler

const (
	ExecutionPolicyCatalogSchemaVersion = "chill.dacs.io/profiler-execution-policies.v1alpha1"
	ExecutionPolicySchemaVersion        = "chill.dacs.io/profiler-execution-policy.v1alpha1"
	FrozenExecutionPolicySchemaVersion  = "chill.dacs.io/frozen-profiler-execution-policy.v1alpha2"

	ConfigMapExecutionPolicySourceKind = "ConfigMap"
)

// ExecutionPolicyCatalog is installation-time worker-delivery configuration.
// Selection is by frozen DeviceClass, execution path, and workload mode, never
// by a caller-supplied resource or timeout override.
type ExecutionPolicyCatalog struct {
	SchemaVersion string                `json:"schemaVersion"`
	Policies      []ExecutionPolicySpec `json:"policies"`
}

// ExecutionPolicySpec is the human-authored form of one worker envelope.
type ExecutionPolicySpec struct {
	Name          string `json:"name"`
	DeviceClass   string `json:"deviceClass"`
	ExecutionPath string `json:"executionPath"`
	WorkloadMode  string `json:"workloadMode"`

	ActiveDeadlineOverheadSeconds  int `json:"activeDeadlineOverheadSeconds"`
	AttemptTimeoutOverheadSeconds  int `json:"attemptTimeoutOverheadSeconds"`
	ObserverReadyTimeoutSeconds    int `json:"observerReadyTimeoutSeconds"`
	ObserverStartOverheadSeconds   int `json:"observerStartOverheadSeconds"`
	MetricResultOverheadSeconds    int `json:"metricResultOverheadSeconds"`
	WorkloadEndFenceTimeoutSeconds int `json:"workloadEndFenceTimeoutSeconds"`
	WorkloadResultTimeoutSeconds   int `json:"workloadResultTimeoutSeconds"`

	MaxLatencySamples      int   `json:"maxLatencySamples"`
	MaxInputBytes          int64 `json:"maxInputBytes"`
	MaxRuntimeResultBytes  int64 `json:"maxRuntimeResultBytes"`
	MaxMetricResultBytes   int64 `json:"maxMetricResultBytes"`
	MaxMetricEvents        int   `json:"maxMetricEvents"`
	MaxWorkloadEvents      int   `json:"maxWorkloadEvents"`
	MaxWorkloadResultBytes int64 `json:"maxWorkloadResultBytes"`

	RuntimeMemoryRequestBytes          int64  `json:"runtimeMemoryRequestBytes"`
	RuntimeMemoryLimitBytes            int64  `json:"runtimeMemoryLimitBytes"`
	ObserverCPURequest                 string `json:"observerCPURequest"`
	ObserverCPULimit                   string `json:"observerCPULimit"`
	ObserverMemoryRequestBytes         int64  `json:"observerMemoryRequestBytes"`
	ObserverMemoryLimitBytes           int64  `json:"observerMemoryLimitBytes"`
	WorkloadObserverCPURequest         string `json:"workloadObserverCPURequest"`
	WorkloadObserverCPULimit           string `json:"workloadObserverCPULimit"`
	WorkloadObserverMemoryRequestBytes int64  `json:"workloadObserverMemoryRequestBytes"`
	WorkloadObserverMemoryLimitBytes   int64  `json:"workloadObserverMemoryLimitBytes"`
	CoordinationReserveBytes           int64  `json:"coordinationReserveBytes"`
	WorkspaceBytes                     int64  `json:"workspaceBytes"`
}

// ExecutionPolicy is the canonical selected worker envelope. Its digest is
// the execution-policy version retained on the immutable Job.
type ExecutionPolicy struct {
	SchemaVersion string `json:"schemaVersion"`
	ExecutionPolicySpec
}

// ExecutionPolicySource identifies the exact mutable catalog snapshot copied
// into the immutable Run record.
type ExecutionPolicySource struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Key             string `json:"key,omitempty"`
}

type FrozenExecutionPolicy struct {
	SchemaVersion string                `json:"schemaVersion"`
	Source        ExecutionPolicySource `json:"source"`
	Policy        ExecutionPolicy       `json:"policy"`
}
