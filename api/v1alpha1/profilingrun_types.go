package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ProfilingRunConditionExecutionComplete reports whether the requested execution completed.
	// It is False when RunConfig resolution rejects the Run or the native Job fails or is lost.
	ProfilingRunConditionExecutionComplete = "ExecutionComplete"
	// ProfilingRunConditionEvidenceVerified reports independent durable evidence verification.
	ProfilingRunConditionEvidenceVerified = "EvidenceVerified"
)

// MeasurementStateSpec fixes the execution state every trial of one
// measurement shares.
type MeasurementStateSpec struct {
	// Candidate selects one static catalog execution path.
	Candidate ProfilingRunCandidateSpec `json:"candidate"`

	// TargetNode names the Kubernetes Node on which the Run is requested.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	TargetNode string `json:"targetNode"`

	// CPURequest is the non-exclusive scheduling request for the runtime container.
	CPURequest resource.Quantity `json:"cpuRequest"`
}

// ProfilingRunSpec requests exactly one immutable profiling execution.
// Materially different inputs require a new ProfilingRun.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ProfilingRun spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.measurement.durationSeconds",message="metricObservation.durationSeconds must cover measurement.durationSeconds"
// +kubebuilder:validation:XValidation:rule="quantity(self.cpuRequest).compareTo(quantity('0')) > 0",message="cpuRequest must be positive"
// +kubebuilder:validation:XValidation:rule="!has(self.measurement.cpuQuota) || quantity(self.cpuRequest).compareTo(quantity(self.measurement.cpuQuota)) <= 0",message="cpuRequest must not exceed measurement.cpuQuota"
type ProfilingRunSpec struct {
	MeasurementStateSpec `json:",inline"`

	// Measurement fixes the single-Run workload and measurement window.
	Measurement ProfilingRunMeasurementSpec `json:"measurement"`

	// MetricObservation fixes polling behavior. The source is resolved from TargetNode.
	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}

type ProfilingRunCandidateSpec struct {
	// ModelSpec names the cluster-scoped ModelSpec.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ModelSpec string `json:"modelSpec"`

	// DeviceClass names the cluster-scoped DeviceClass.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	DeviceClass string `json:"deviceClass"`

	// ExecutionPath selects one path declared by ModelSpec.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ExecutionPath string `json:"executionPath"`

	// PowerMode selects one mode declared by DeviceClass. Fixed Runs may execute
	// directly; a non-fixed selection requires a selected configured actuation
	// policy and a completed transition that confirms the required state.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PowerMode string `json:"powerMode"`

	// Preprocessor names the cluster-scoped Preprocessor whose artifact for the
	// selected model artifact runs beside the model.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Preprocessor string `json:"preprocessor"`
}

// +kubebuilder:validation:XValidation:rule="self.workloadMode == 'idle-model-loaded' ? self.warmupIterations == 0 && has(self.settlingDurationSeconds) && !has(self.dataset) : !has(self.settlingDurationSeconds) && has(self.dataset)",message="measurement fields must match the selected workloadMode"
// +kubebuilder:validation:XValidation:rule="!has(self.cpuQuota) || quantity(self.cpuQuota).compareTo(quantity('0')) > 0",message="cpuQuota must be positive"
// +kubebuilder:validation:XValidation:rule="!has(self.cpuQuota) || quantity(self.cpuQuota).isInteger()",message="cpuQuota must be a whole number of CPU cores"
type ProfilingRunMeasurementSpec struct {
	// +kubebuilder:validation:Minimum=1
	Batch int32 `json:"batch"`

	// +kubebuilder:validation:Minimum=0
	WarmupIterations int32 `json:"warmupIterations"`

	// CPUQuota optionally selects a configured whole-core runtime quota. It is
	// consumed while freezing the Run and is not duplicated in MeasurementContract.
	// +optional
	CPUQuota *resource.Quantity `json:"cpuQuota,omitempty"`

	// CPUAffinity optionally selects explicit Linux CPU IDs for firstfloor execution.
	// It does not reserve CPUs or claim exclusivity.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Minimum=0
	// +listType=set
	CPUAffinity []int32 `json:"cpuAffinity,omitempty"`

	// SettlingDurationSeconds is required only for model-loaded idle. The
	// runtime loads the model before this observer-independent settling period.
	// +optional
	// +kubebuilder:validation:Minimum=1
	SettlingDurationSeconds *int32 `json:"settlingDurationSeconds,omitempty"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	// +kubebuilder:validation:Enum=saturated-serial-fixed-batch;idle-model-loaded
	WorkloadMode string `json:"workloadMode"`

	// Dataset names the cluster-scoped Dataset whose profiling records active
	// measurement samples. Idle measurement has none.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Dataset string `json:"dataset,omitempty"`
}

// ProfilingRunMetricObservationSpec contains policy only, never an endpoint.
// +kubebuilder:validation:XValidation:rule="self.timeoutMilliseconds < self.intervalMilliseconds",message="timeoutMilliseconds must be less than intervalMilliseconds"
type ProfilingRunMetricObservationSpec struct {
	// +kubebuilder:validation:Minimum=1
	IntervalMilliseconds int32 `json:"intervalMilliseconds"`

	// +kubebuilder:validation:Minimum=1
	TimeoutMilliseconds int32 `json:"timeoutMilliseconds"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`
}

// ProfilingRunStatus contains compact, reconstructable Run outcomes.
type ProfilingRunStatus struct {
	// ObservedGeneration is the latest spec generation processed by the Profiler.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions separately report execution and evidence verification.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Job identifies the one owned native Job after creation.
	// +optional
	Job *ProfilingRunObjectReference `json:"job,omitempty"`

	// Records the Pod and Node identities observed after the Run was scheduled.
	// +optional
	ObservedExecution *ProfilingRunObservedExecutionStatus `json:"actualExecution,omitempty"`

	// Evidence identifies independently verified immutable execution evidence.
	// +optional
	Evidence *ProfilingRunEvidenceReference `json:"evidence,omitempty"`
}

// ProfilingRunObjectReference is deliberately smaller than corev1.ObjectReference.
type ProfilingRunObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	UID string `json:"uid"`
}

type ProfilingRunObservedExecutionStatus struct {
	Pod  ProfilingRunObjectReference `json:"pod"`
	Node ProfilingRunObjectReference `json:"node"`
}

type ProfilingRunEvidenceReference struct {
	// Store is the logical CHILL-managed store identity, not an endpoint.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Store string `json:"store"`

	// Key is opaque to API consumers.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Key string `json:"key"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SchemaVersion string `json:"schemaVersion"`

	// +kubebuilder:validation:Minimum=1
	Size int64 `json:"size"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=prun
// +kubebuilder:printcolumn:name="Execution",type=string,JSONPath=`.status.conditions[?(@.type=="ExecutionComplete")].status`
// +kubebuilder:printcolumn:name="Evidence",type=string,JSONPath=`.status.conditions[?(@.type=="EvidenceVerified")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ProfilingRun requests and reports one immutable profiling execution.
type ProfilingRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProfilingRunSpec   `json:"spec"`
	Status ProfilingRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ProfilingRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProfilingRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProfilingRun{}, &ProfilingRunList{})
}
