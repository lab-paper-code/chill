package v1alpha1

import (
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
}

// ProfilingRunSpec requests exactly one immutable profiling execution.
// Materially different inputs require a new ProfilingRun.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ProfilingRun spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.measurement.durationSeconds",message="metricObservation.durationSeconds must cover measurement.durationSeconds"
type ProfilingRunSpec struct {
	MeasurementStateSpec `json:",inline"`

	// Measurement fixes the single-Run workload and measurement window.
	Measurement ProfilingRunMeasurementSpec `json:"measurement"`

	// CampaignTrial identifies the experiment's logical batch and attempt,
	// independently of Measurement.Batch (the actual execution batch).
	// Required by BatchEnergyOptimization-owned Runs; absent for standalone Runs.
	// +optional
	CampaignTrial *ProfilingRunCampaignTrialSpec `json:"campaignTrial,omitempty"`

	// MetricObservation fixes polling behavior. The source is resolved from TargetNode.
	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}

type ProfilingRunCampaignTrialSpec struct {
	// Batch is the logical batch whose active trial or idle baseline this Run measures.
	// +kubebuilder:validation:Minimum=1
	Batch int32 `json:"batch"`

	// +kubebuilder:validation:Minimum=1
	Attempt int32 `json:"attempt"`
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

// +kubebuilder:validation:XValidation:rule="!has(self.durationToleranceSeconds) || self.durationToleranceSeconds < double(self.durationSeconds)",message="duration tolerance must be less than duration"
// +kubebuilder:validation:XValidation:rule="self.workloadMode == 'idle-model-loaded' ? self.warmupIterations == 0 && has(self.settlingDurationSeconds) && !has(self.dataset) && !has(self.workload) : !has(self.settlingDurationSeconds) && has(self.dataset) && has(self.workload)",message="measurement fields must match the selected workloadMode"
// +kubebuilder:validation:XValidation:rule="self.workloadMode == 'idle-model-loaded' || self.durationSeconds >= 30",message="active measurement requires at least thirty seconds"
// +kubebuilder:validation:XValidation:rule="self.workloadMode == 'idle-model-loaded' ? !has(self.operatingPoints) : has(self.operatingPoints)",message="operatingPoints is required only for active measurement"
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

	// DurationToleranceSeconds bounds measurement-window undershoot.
	// +optional
	// +kubebuilder:default=0.5
	// +kubebuilder:validation:Minimum=0
	DurationToleranceSeconds *float64 `json:"durationToleranceSeconds,omitempty"`

	// +kubebuilder:validation:Enum=empirical-cadence-fixed-batch;idle-model-loaded
	WorkloadMode string `json:"workloadMode"`

	// Dataset names the cluster-scoped Dataset whose profiling records active
	// measurement samples. Idle measurement has none.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Dataset string `json:"dataset,omitempty"`

	// Workload names the cluster-scoped source of the active stop-policy SLO.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Workload string `json:"workload,omitempty"`

	// WarmupBatchSizes freezes the campaign's complete planned warmup domain.
	// +optional
	// +kubebuilder:validation:items:Minimum=1
	WarmupBatchSizes []int32 `json:"warmupBatchSizes,omitempty"`

	// StartupWarmupIterations is derived from the campaign's active protocol,
	// including for idle actors; it is not the idle measurement call count.
	// +optional
	// +kubebuilder:validation:Minimum=0
	StartupWarmupIterations int32 `json:"startupWarmupIterations,omitempty"`

	// OperatingPoints is required for active measurement and absent for idle.
	// +optional
	OperatingPoints *MeasurementOperatingPointsSpec `json:"operatingPoints,omitempty"`
}

// MeasurementOperatingPointsSpec selects reproducible off-node native load.
type MeasurementOperatingPointsSpec struct {
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:ExclusiveMinimum=true
	// +kubebuilder:validation:items:Minimum=0
	// +kubebuilder:validation:items:ExclusiveMaximum=true
	// +kubebuilder:validation:items:Maximum=1
	Utilizations []float64 `json:"utilizations"`
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Minimum=0
	SparseMeanGapMilliseconds float64 `json:"sparseMeanGapMilliseconds"`
	// +kubebuilder:validation:Minimum=30
	PhaseDurationSeconds int32 `json:"phaseDurationSeconds"`
	// +kubebuilder:validation:Minimum=1
	RequestLifetimeSeconds int32 `json:"requestLifetimeSeconds"`
	// +kubebuilder:validation:Minimum=1
	DeliveryMarginMilliseconds int32 `json:"deliveryMarginMilliseconds"`
	// +kubebuilder:validation:Minimum=1
	FramedDecodedRequestUpperBytes int64 `json:"framedDecodedRequestUpperBytes"`
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

	// EvidenceStore retains the frozen delivery binding after native Job cleanup.
	// +optional
	EvidenceStore *ProfilingRunEvidenceStoreBinding `json:"evidenceStore,omitempty"`

	// JobCleanup records whether terminal native Job cleanup remains pending.
	// +optional
	// +kubebuilder:validation:Enum=Pending;Done
	JobCleanup string `json:"jobCleanup,omitempty"`

	// RuntimeTermination keeps the actual failed container, native Job cause,
	// proven worker phase, exit code and log tail after its Pod is deleted.
	// +optional
	RuntimeTermination *ProfilingRunRuntimeTermination `json:"runtimeTermination,omitempty"`
}

// HasTerminalFailure reports whether this attempt ended in a known execution
// or evidence failure. It does not choose the parent's next action.
func (s ProfilingRunStatus) HasTerminalFailure() bool {
	for _, condition := range s.Conditions {
		if isProfilingRunTerminalFailure(condition) {
			return true
		}
	}
	return false
}

// TerminalFailureReasons interprets failed execution/evidence conditions for
// parent controllers. Evidence precedence and retry/publication policy remain
// with the caller. Immutable Runs retain the parents' existing generation semantics.
func (s ProfilingRunStatus) TerminalFailureReasons() []string {
	var reasons []string
	for _, condition := range s.Conditions {
		if isProfilingRunTerminalFailure(condition) {
			reasons = append(reasons, condition.Reason)
		}
	}
	return reasons
}

func isProfilingRunTerminalFailure(condition metav1.Condition) bool {
	if condition.Status != metav1.ConditionFalse ||
		(condition.Type != ProfilingRunConditionExecutionComplete &&
			condition.Type != ProfilingRunConditionEvidenceVerified) {
		return false
	}
	switch condition.Reason {
	case "InvalidRunSpec", "ExecutionPolicyInvalid", "ActuationPolicyInvalid",
		"ExecutionLost", "JobFailed", "RequiredStateNotConfirmed", "EvidenceInvalid":
		return true
	default:
		return false
	}
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

type ProfilingRunRuntimeTermination struct {
	ExitCode int32 `json:"exitCode"`

	// Class separates expected discovery stops, known transient disruption,
	// terminal failures, and failures whose attribution remains unknown.
	// +kubebuilder:validation:Enum=ExpectedStop;RetryableFailure;FatalFailure;UnknownFailure
	// +optional
	Class string `json:"class,omitempty"`

	// Container and Reason retain the native container identity and termination reason.
	// +optional
	Container string `json:"container,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// Stage is proven by the versioned worker termination message, never by log text.
	// +optional
	Stage string `json:"stage,omitempty"`
	// FailureReason retains a typed worker failure or an explicit native disruption reason.
	// +optional
	FailureReason string `json:"failureReason,omitempty"`
	// JobReason and JobMessage retain the native failed Job condition.
	// +optional
	JobReason string `json:"jobReason,omitempty"`
	// +optional
	JobMessage string `json:"jobMessage,omitempty"`
	// PodReason and PodMessage retain the native failed Pod reason or its
	// explicit disruption condition, independently of the container reason.
	// +optional
	PodReason string `json:"podReason,omitempty"`
	// +optional
	PodMessage string `json:"podMessage,omitempty"`

	// LogTail is the runtime termination message, which Kubernetes fills
	// with the last 80 lines or 2 KiB of the container log on failure.
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	LogTail string `json:"logTail,omitempty"`
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

// ProfilingRunEvidenceStoreBinding contains no credentials, only their Secret name.
type ProfilingRunEvidenceStoreBinding struct {
	// +kubebuilder:validation:MinLength=1
	Store string `json:"store"`

	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// +kubebuilder:validation:MinLength=1
	Bucket string `json:"bucket"`

	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`

	// +kubebuilder:validation:MinLength=1
	CredentialsSecretName string `json:"credentialsSecretName"`
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
