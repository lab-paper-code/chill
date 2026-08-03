package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ProfilingRunConditionExecutionComplete reports whether the requested execution completed.
	// It is False when intent freezing rejects the Run or the native Job fails or is lost.
	ProfilingRunConditionExecutionComplete = "ExecutionComplete"
	// ProfilingRunConditionEvidenceVerified reports independent durable evidence verification.
	ProfilingRunConditionEvidenceVerified = "EvidenceVerified"
	// ProfilingRunConditionEvidenceAdmitted reports scientific single-Run admission.
	ProfilingRunConditionEvidenceAdmitted = "EvidenceAdmitted"
)

// ProfilingRunSpec requests exactly one immutable profiling execution.
// Materially different inputs require a new ProfilingRun.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ProfilingRun spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.measurement.durationSeconds",message="metricObservation.durationSeconds must cover measurement.durationSeconds"
// +kubebuilder:validation:XValidation:rule="quantity(self.cpuRequest).compareTo(quantity('0')) > 0",message="cpuRequest must be positive"
type ProfilingRunSpec struct {
	// Candidate selects one static catalog execution path.
	Candidate ProfilingRunCandidateSpec `json:"candidate"`

	// TargetNode names the Kubernetes Node on which the Run is requested.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	TargetNode string `json:"targetNode"`

	// CPURequest is the non-exclusive scheduling request for the runtime container.
	CPURequest resource.Quantity `json:"cpuRequest"`

	// Measurement fixes the single-Run workload and measurement window.
	Measurement ProfilingRunMeasurementSpec `json:"measurement"`

	// MetricObservation fixes polling behavior. The source is resolved from TargetNode.
	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`

	// AdmissionPolicy identifies the configured single-Run scientific admission policy.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	AdmissionPolicy string `json:"admissionPolicy"`
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

	// PowerMode selects one mode declared by DeviceClass. The first supported
	// CPU path accepts only the non-actuated "fixed" mode.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PowerMode string `json:"powerMode"`
}

// +kubebuilder:validation:XValidation:rule="self.workloadMode == 'idle-model-loaded' ? self.batch == 1 && self.warmupIterations == 0 && has(self.settlingDurationSeconds) && self.inputGenerator == 'none' && self.outputValidation == 'none' : !has(self.settlingDurationSeconds) && self.inputGenerator == 'numpy-default-rng-seed-0-fp32-v1' && self.outputValidation == 'nonempty-shape-batch-v1'",message="measurement fields must match the selected workloadMode"
type ProfilingRunMeasurementSpec struct {
	// +kubebuilder:validation:Minimum=1
	Batch int32 `json:"batch"`

	// +kubebuilder:validation:Minimum=0
	WarmupIterations int32 `json:"warmupIterations"`

	// SettlingDurationSeconds is required only for model-loaded idle. The
	// runtime loads the model before this observer-independent settling period.
	// +optional
	// +kubebuilder:validation:Minimum=1
	SettlingDurationSeconds *int32 `json:"settlingDurationSeconds,omitempty"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	// The first path treats each ProfilingRun as one independent trial.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1
	Repetitions int32 `json:"repetitions"`

	// +kubebuilder:validation:Enum=saturated-serial-fixed-batch;idle-model-loaded
	WorkloadMode string `json:"workloadMode"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	InputGenerator string `json:"inputGenerator"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	OutputValidation string `json:"outputValidation"`
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

	// Conditions separately report execution, evidence verification, and admission.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// IntentDigest identifies the exact frozen execution intent.
	// +optional
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	IntentDigest string `json:"intentDigest,omitempty"`

	// Job identifies the one owned native Job after creation.
	// +optional
	Job *ProfilingRunObjectReference `json:"job,omitempty"`

	// ActualExecution records the Pod and Node identities observed after scheduling.
	// +optional
	ActualExecution *ProfilingRunActualExecutionStatus `json:"actualExecution,omitempty"`

	// Evidence identifies independently verified immutable execution evidence.
	// +optional
	Evidence *ProfilingRunEvidenceReference `json:"evidence,omitempty"`

	// Admission records the evaluated scientific verdict without raw evidence.
	// +optional
	Admission *ProfilingRunAdmissionStatus `json:"admission,omitempty"`
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

type ProfilingRunActualExecutionStatus struct {
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

	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	Digest string `json:"digest"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SchemaVersion string `json:"schemaVersion"`

	// +kubebuilder:validation:Minimum=1
	Size int64 `json:"size"`
}

type ProfilingRunAdmissionStatus struct {
	// +kubebuilder:validation:Enum=Accepted;Rejected;Insufficient
	Verdict string `json:"verdict"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PolicyVersion string `json:"policyVersion"`

	// ReasonCodes contains stable typed codes, not raw diagnostic messages.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=128
	// +listType=set
	ReasonCodes []string `json:"reasonCodes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=prun
// +kubebuilder:printcolumn:name="Execution",type=string,JSONPath=`.status.conditions[?(@.type=="ExecutionComplete")].status`
// +kubebuilder:printcolumn:name="Evidence",type=string,JSONPath=`.status.conditions[?(@.type=="EvidenceVerified")].status`
// +kubebuilder:printcolumn:name="Admitted",type=string,JSONPath=`.status.conditions[?(@.type=="EvidenceAdmitted")].status`
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
