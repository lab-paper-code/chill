package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// MeasurementPhase is the phase of a measurement ChillServer runs on one Node.
// +kubebuilder:validation:Enum=Measuring;Complete;Insufficient;Failed
type MeasurementPhase string

const (
	MeasurementPhaseMeasuring    MeasurementPhase = "Measuring"
	MeasurementPhaseComplete     MeasurementPhase = "Complete"
	MeasurementPhaseInsufficient MeasurementPhase = "Insufficient"
	MeasurementPhaseFailed       MeasurementPhase = "Failed"
)

// OperationCostStepKind is one atomic operation of an operation-cost measurement.
// +kubebuilder:validation:Enum=power;prepare;idle;remove
type OperationCostStepKind string

const (
	OperationCostStepPower   OperationCostStepKind = "power"
	OperationCostStepPrepare OperationCostStepKind = "prepare"
	OperationCostStepIdle    OperationCostStepKind = "idle"
	OperationCostStepRemove  OperationCostStepKind = "remove"
)

// OperationCostMeasurementSpec declares the ordered atomic operations measured
// on one Node; ChillServer measures them in order and records each one.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="OperationCostMeasurement spec is immutable"
type OperationCostMeasurementSpec struct {
	// Node names the Kubernetes Node measured.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Node string `json:"node"`

	Observation MeasurementObservationSpec `json:"observation"`

	// Steps are measured in order; the measurement stops at the first step
	// that fails or is not Accepted.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=name
	Steps []OperationCostStep `json:"steps"`
}

// MeasurementObservationSpec freezes wall-power sampling and empirical timing
// inputs for every step.
// +kubebuilder:validation:XValidation:rule="self.requestTimeoutMilliseconds < self.intervalMilliseconds",message="requestTimeoutMilliseconds must be less than intervalMilliseconds"
type MeasurementObservationSpec struct {
	// +kubebuilder:validation:Minimum=1
	IntervalMilliseconds int32 `json:"intervalMilliseconds"`
	// +kubebuilder:validation:Minimum=1
	RequestTimeoutMilliseconds int32 `json:"requestTimeoutMilliseconds"`
	// MaximumOperationSeconds bounds the physical action, excluding pre/post capture.
	// +kubebuilder:validation:Minimum=1
	MaximumOperationSeconds int32 `json:"maximumOperationSeconds"`
	// Timing freezes the required empirical observation inputs.
	Timing *MeasurementTimingSpec `json:"timing"`
	// MaximumGapMilliseconds flags full original long gaps for missing correction,
	// rather than rejecting their coverage.
	// +kubebuilder:validation:Minimum=1
	MaximumGapMilliseconds int32 `json:"maximumGapMilliseconds"`
}

// MeasurementTimingSpec retains the unrounded empirical observation inputs.
// A is their sum, rounded upward only when represented as runtime nanoseconds.
// Unchanged dwell is deliberately not an input.
type MeasurementTimingSpec struct {
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	ChangeIntervalP95Seconds string `json:"changeIntervalP95Seconds"`
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	MaximumRequestSeconds string `json:"maximumRequestSeconds"`
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	MaximumReceiptGapSeconds string `json:"maximumReceiptGapSeconds"`
	// Source identifies the completed observation and any device transfer.
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`
}

// OperationCostStep is one atomic operation; only the fields of its kind are set.
// +kubebuilder:validation:XValidation:rule="(self.kind == 'power') == has(self.powerMode)",message="powerMode is set exactly for power"
// +kubebuilder:validation:XValidation:rule="(self.kind == 'prepare' || self.kind == 'remove') == has(self.batchEnergyOptimization)",message="batchEnergyOptimization is set exactly for prepare and remove"
// +kubebuilder:validation:XValidation:rule="(self.kind == 'idle') == has(self.idleDurationSeconds)",message="idleDurationSeconds is set exactly for idle"
type OperationCostStep struct {
	// Name is the step identity in its record key.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string                `json:"name"`
	Kind OperationCostStepKind `json:"kind"`
	// PowerMode is the mode a power step switches the empty Node into.
	// +optional
	// +kubebuilder:validation:MinLength=1
	PowerMode string `json:"powerMode,omitempty"`
	// BatchEnergyOptimization names the measured option a prepare or remove step uses.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	BatchEnergyOptimization string `json:"batchEnergyOptimization,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	IdleDurationSeconds *int32 `json:"idleDurationSeconds,omitempty"`
}

type OperationCostMeasurementStatus struct {
	// +optional
	Phase MeasurementPhase `json:"phase,omitempty"`
	// Reason explains a phase other than Measuring and Complete.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Steps are the saved step records in order.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Steps []OperationCostStepStatus `json:"steps,omitempty"`
}

type OperationCostStepStatus struct {
	Name    string `json:"name"`
	Key     string `json:"key"`
	Verdict string `json:"verdict"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=opcost
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.node`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OperationCostMeasurement requests the ordered measurement of atomic
// operations on one Node through measurement admission.
type OperationCostMeasurement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OperationCostMeasurementSpec   `json:"spec"`
	Status OperationCostMeasurementStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type OperationCostMeasurementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OperationCostMeasurement `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OperationCostMeasurement{}, &OperationCostMeasurementList{})
}
