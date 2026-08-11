package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const MethodSequentialEarlyStop = "sequential-early-stop"

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="BatchSaturation spec is immutable"
type BatchSaturationSpec struct {
	State          MeasurementStateSpec  `json:"state"`
	ActiveProtocol ActiveProtocolSpec    `json:"activeProtocol"`
	IdleProtocol   IdleProtocolSpec      `json:"idleProtocol"`
	Method         MeasurementMethodSpec `json:"method"`

	// RetryLimit bounds attempts per batch that did not reach an Accepted
	// verdict. It does not bound admitted trials.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	RetryLimit int32 `json:"retryLimit"`
}

// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.durationSeconds",message="metricObservation.durationSeconds must cover durationSeconds"
type ActiveProtocolSpec struct {
	// +kubebuilder:validation:Minimum=0
	WarmupIterations int32 `json:"warmupIterations"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	InputGenerator string `json:"inputGenerator"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	OutputValidation string `json:"outputValidation"`

	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}

// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.durationSeconds",message="metricObservation.durationSeconds must cover durationSeconds"
type IdleProtocolSpec struct {
	// +kubebuilder:validation:Minimum=1
	SettlingDurationSeconds int32 `json:"settlingDurationSeconds"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}

// MeasurementMethodSpec selects one measurement method and carries exactly that
// method's parameters.
// +kubebuilder:validation:XValidation:rule="self.name == 'sequential-early-stop' ? has(self.sequentialEarlyStop) : !has(self.sequentialEarlyStop)",message="method parameters must match the selected method name"
type MeasurementMethodSpec struct {
	// +kubebuilder:validation:Enum=sequential-early-stop
	Name string `json:"name"`

	// +optional
	SequentialEarlyStop *SequentialEarlyStopSpec `json:"sequentialEarlyStop,omitempty"`
}

type SequentialEarlyStopSpec struct {
	// MinDelta is the relative improvement below which a batch does not count
	// as an improvement over the best batch so far.
	MinDelta string `json:"minDelta"`

	// +kubebuilder:validation:Minimum=1
	Patience int32 `json:"patience"`

	// +kubebuilder:validation:Minimum=3
	TrialsPerBatch int32 `json:"trialsPerBatch"`

	// DivergenceFactor is the multiple of the best value above which the sweep
	// stops early. Must exceed 1.
	DivergenceFactor string `json:"divergenceFactor"`
}

type BatchSaturationStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum=Measuring;Complete;Failed
	Phase string `json:"phase,omitempty"`

	// +optional
	AcceptedTrials int32 `json:"acceptedTrials,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=128
	Outcome string `json:"outcome,omitempty"`

	// +optional
	Result *ProfilingRunEvidenceReference `json:"result,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=bsat
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Trials",type=integer,JSONPath=`.status.acceptedTrials`
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=`.status.outcome`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BatchSaturation drives one batch-saturation measurement by owning the
// ProfilingRun objects it creates.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 200",message="name must be at most 200 characters so generated Run names fit"
type BatchSaturation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BatchSaturationSpec   `json:"spec"`
	Status BatchSaturationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type BatchSaturationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BatchSaturation `json:"items"`
}

func init() { SchemeBuilder.Register(&BatchSaturation{}, &BatchSaturationList{}) }
