package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="BatchEnergyOptimization spec is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.activeProtocol.admissionPolicy) || !has(self.idleProtocol.admissionPolicy) || self.activeProtocol.admissionPolicy != self.idleProtocol.admissionPolicy",message="active and idle Runs require policies for different workload modes"
type BatchEnergyOptimizationSpec struct {
	State          MeasurementStateSpec `json:"state"`
	ActiveProtocol ActiveProtocolSpec   `json:"activeProtocol"`
	IdleProtocol   IdleProtocolSpec     `json:"idleProtocol"`

	// TrialsPerBatch is the number of admitted active and idle observations used
	// to compare each batch under the minimum-energy objective.
	// +kubebuilder:validation:Minimum=3
	TrialsPerBatch int32 `json:"trialsPerBatch"`

	// BatchSizes is the measurement-selected sweep for an artifact that does
	// not declare a finite batch-size set. It must be absent when the selected
	// artifact declares its own set. Kubernetes forbids uniqueItems on an array
	// schema, so ascending order is checked here and strict increase is enforced
	// where the sweep is resolved.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self.isSorted()",message="batchSizes must be in ascending order"
	BatchSizes []int32 `json:"batchSizes,omitempty"`

	// RetryLimit bounds attempts per batch that did not reach an Accepted
	// verdict. It does not bound admitted trials.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	RetryLimit int32 `json:"retryLimit"`
}

// +kubebuilder:validation:XValidation:rule="self.metricObservation.durationSeconds >= self.durationSeconds",message="metricObservation.durationSeconds must cover durationSeconds"
type ActiveProtocolSpec struct {
	// AdmissionPolicy identifies the configured admission policy for active Runs.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	AdmissionPolicy string `json:"admissionPolicy"`

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
	// AdmissionPolicy identifies the configured admission policy for idle Runs.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	AdmissionPolicy string `json:"admissionPolicy"`

	// +kubebuilder:validation:Minimum=1
	SettlingDurationSeconds int32 `json:"settlingDurationSeconds"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	MetricObservation ProfilingRunMetricObservationSpec `json:"metricObservation"`
}

type BatchEnergyOptimizationStatus struct {
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
// +kubebuilder:resource:scope=Namespaced,shortName=beo
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Trials",type=integer,JSONPath=`.status.acceptedTrials`
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=`.status.outcome`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BatchEnergyOptimization searches one batch-size space for the minimum
// incremental energy per completed item by owning the ProfilingRun objects it
// creates.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 200",message="name must be at most 200 characters so generated Run names fit"
type BatchEnergyOptimization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BatchEnergyOptimizationSpec   `json:"spec"`
	Status BatchEnergyOptimizationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type BatchEnergyOptimizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BatchEnergyOptimization `json:"items"`
}

func init() { SchemeBuilder.Register(&BatchEnergyOptimization{}, &BatchEnergyOptimizationList{}) }
