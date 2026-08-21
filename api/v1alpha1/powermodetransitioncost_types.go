package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type PowerModeTransitionCostPhase string

const (
	PowerModeTransitionCostPhasePending   PowerModeTransitionCostPhase = "Pending"
	PowerModeTransitionCostPhaseMeasuring PowerModeTransitionCostPhase = "Measuring"
	PowerModeTransitionCostPhaseComplete  PowerModeTransitionCostPhase = "Complete"
	PowerModeTransitionCostPhaseFailed    PowerModeTransitionCostPhase = "Failed"
)

// PowerModeTransitionCostSpec fixes one repeated measurement of both
// directions between exactly two power modes.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="PowerModeTransitionCost spec is immutable"
// +kubebuilder:validation:XValidation:rule="self.modes[0].powerMode != self.modes[1].powerMode",message="modes must name distinct power modes"
// +kubebuilder:validation:XValidation:rule="self.attemptLimit >= self.acceptedCountTarget",message="attemptLimit must be at least acceptedCountTarget"
type PowerModeTransitionCostSpec struct {
	// TargetNode names the Kubernetes Node being measured.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	TargetNode string `json:"targetNode"`

	// DeviceClass names the cluster-scoped DeviceClass.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	DeviceClass string `json:"deviceClass"`

	// ModelSpec names the cluster-scoped ModelSpec already serving on the Node.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ModelSpec string `json:"modelSpec"`

	// ExecutionPath selects the ModelSpec path shared by the serving workload
	// and target-mode idle baselines.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ExecutionPath string `json:"executionPath"`

	// Modes defines the two directions in order: modes[0] to modes[1], then
	// modes[1] to modes[0]. Each baseline is for transitions targeting that mode.
	// +kubebuilder:validation:MinItems=2
	// +kubebuilder:validation:MaxItems=2
	Modes []PowerModeTransitionCostModeSpec `json:"modes"`

	// ServingService selects the namespaced Service whose selected Pod readiness is observed.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ServingService string `json:"servingService"`

	// ServingContainer names the serving container whose process survival is observed.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	ServingContainer string `json:"servingContainer"`

	// AcceptedCountTarget is required independently for each direction.
	// +kubebuilder:validation:Minimum=5
	AcceptedCountTarget int32 `json:"acceptedCountTarget"`

	// AttemptLimit bounds retained attempts independently for each direction.
	// +kubebuilder:validation:Minimum=5
	AttemptLimit int32 `json:"attemptLimit"`

	ObservationPolicy PowerModeTransitionCostObservationPolicySpec `json:"observationPolicy"`
}

type PowerModeTransitionCostModeSpec struct {
	// PowerMode selects one state. Preparation resolves and freezes the unique
	// configured actuation policy and current Complete exact-state idle baseline.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PowerMode string `json:"powerMode"`
}

// PowerModeTransitionCostObservationPolicySpec contains measurement policy
// only. Meter source and series selection are resolved outside this API.
// +kubebuilder:validation:XValidation:rule="self.powerTimeoutMilliseconds < self.powerIntervalMilliseconds",message="powerTimeoutMilliseconds must be less than powerIntervalMilliseconds"
type PowerModeTransitionCostObservationPolicySpec struct {
	// +kubebuilder:validation:Minimum=1
	PowerIntervalMilliseconds int32 `json:"powerIntervalMilliseconds"`

	// +kubebuilder:validation:Minimum=1
	PowerTimeoutMilliseconds int32 `json:"powerTimeoutMilliseconds"`

	// +kubebuilder:validation:Minimum=1
	MaximumTransitionDurationSeconds int32 `json:"maximumTransitionDurationSeconds"`

	// FactPollIntervalMilliseconds fixes the Kubernetes fact-observation cadence.
	// +kubebuilder:validation:Minimum=1
	FactPollIntervalMilliseconds int32 `json:"factPollIntervalMilliseconds"`

	// +kubebuilder:validation:Minimum=0
	MaximumBoundaryDistanceMilliseconds int32 `json:"maximumBoundaryDistanceMilliseconds"`

	// MinimumCoverage is a decimal string in the interval (0, 1].
	// +kubebuilder:validation:Pattern=`^(0\.[0-9]*[1-9][0-9]*|1(\.0+)?)$`
	MinimumCoverage string `json:"minimumCoverage"`

	// +kubebuilder:validation:Minimum=1
	MaximumSampleGapMilliseconds int32 `json:"maximumSampleGapMilliseconds"`

	// +kubebuilder:validation:Minimum=0
	MaximumBoundaryUncertaintyMilliseconds int32 `json:"maximumBoundaryUncertaintyMilliseconds"`

	// +kubebuilder:validation:Minimum=1
	MaximumReadyObservationDelayMilliseconds int32 `json:"maximumReadyObservationDelayMilliseconds"`
}

type PowerModeTransitionCostStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Pending;Measuring;Complete;Failed
	Phase PowerModeTransitionCostPhase `json:"phase,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=2
	Progress []PowerModeTransitionCostDirectionProgress `json:"progress,omitempty"`

	// +optional
	Result *ProfilingRunEvidenceReference `json:"result,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.acceptedCount <= self.attemptCount",message="acceptedCount must not exceed attemptCount"
type PowerModeTransitionCostDirectionProgress struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SourceMode string `json:"sourceMode"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	TargetMode string `json:"targetMode"`

	// +kubebuilder:validation:Minimum=0
	AttemptCount int32 `json:"attemptCount"`

	// +kubebuilder:validation:Minimum=0
	AcceptedCount int32 `json:"acceptedCount"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pmtc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PowerModeTransitionCost requests and reports one repeated measurement of both
// directed transitions between two power modes.
type PowerModeTransitionCost struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PowerModeTransitionCostSpec   `json:"spec"`
	Status PowerModeTransitionCostStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type PowerModeTransitionCostList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PowerModeTransitionCost `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PowerModeTransitionCost{}, &PowerModeTransitionCostList{})
}
