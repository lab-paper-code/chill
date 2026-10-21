package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DispatchOccupancyMeasurementSpec declares one saturation measurement of the
// time a request occupies an instance outside its batch execution, for the
// Node, power mode, runtime image and model of one measured option.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="DispatchOccupancyMeasurement spec is immutable"
type DispatchOccupancyMeasurementSpec struct {
	// Node names the Kubernetes Node measured.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Node string `json:"node"`

	// BatchEnergyOptimization names the measured option; its profile fixes the
	// power mode, runtime image and model.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	BatchEnergyOptimization string `json:"batchEnergyOptimization"`

	// Workload names the Workload whose requests the load sends.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Workload string `json:"workload"`

	// Batch is the fixed batch size B of the measured instance.
	// +kubebuilder:validation:Minimum=1
	Batch int32 `json:"batch"`

	// RateFactor multiplies the profile throughput at B into the offered rate.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +kubebuilder:validation:XValidation:rule="double(self) >= 1.2",message="rateFactor must be at least 1.2"
	RateFactor string `json:"rateFactor"`

	// WarmupSeconds are excluded from the start of the window.
	// +kubebuilder:validation:Minimum=0
	WarmupSeconds int32 `json:"warmupSeconds"`

	// +kubebuilder:validation:Minimum=60
	DurationSeconds int32 `json:"durationSeconds"`

	// MinimumBatches is the least number of batches the window must hold.
	// +kubebuilder:validation:Minimum=2
	MinimumBatches int32 `json:"minimumBatches"`

	// MaximumIdleFraction bounds the batches after which the instance waited.
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]+)?|1(\.0+)?)$`
	MaximumIdleFraction string `json:"maximumIdleFraction"`

	// Workers is the number of concurrent load generator senders.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4096
	Workers int32 `json:"workers"`
}

type DispatchOccupancyMeasurementStatus struct {
	// +optional
	Phase MeasurementPhase `json:"phase,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// PowerMode is the profile mode the dispatch occupancy belongs to.
	// +optional
	PowerMode string `json:"powerMode,omitempty"`
	// PerRequestNanos is the measured dispatch occupancy d.
	// +optional
	PerRequestNanos *int64 `json:"perRequestNanos,omitempty"`
	// Throughput is the sustained completions per second X.
	// +optional
	Throughput string `json:"throughput,omitempty"`
	// +optional
	Result *ProfilingRunEvidenceReference `json:"result,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=occupancy
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.node`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.status.powerMode`
// +kubebuilder:printcolumn:name="PerRequest",type=integer,JSONPath=`.status.perRequestNanos`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DispatchOccupancyMeasurement requests one dispatch occupancy measurement
// through measurement admission.
type DispatchOccupancyMeasurement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DispatchOccupancyMeasurementSpec   `json:"spec"`
	Status DispatchOccupancyMeasurementStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type DispatchOccupancyMeasurementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DispatchOccupancyMeasurement `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DispatchOccupancyMeasurement{}, &DispatchOccupancyMeasurementList{})
}
