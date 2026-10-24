package v1alpha1

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

	// RateFactor applies only to legacy open-loop measurements.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +kubebuilder:validation:XValidation:rule="double(self) >= 1.2",message="rateFactor must be at least 1.2"
	RateFactor string `json:"rateFactor,omitempty"`

	// WarmupSeconds are excluded from the start of the window.
	// +kubebuilder:validation:Minimum=0
	WarmupSeconds int32 `json:"warmupSeconds"`

	// +kubebuilder:validation:Minimum=60
	DurationSeconds int32 `json:"durationSeconds"`

	// MinimumBatches is the least number of batches the window must hold.
	// +kubebuilder:validation:Minimum=2
	MinimumBatches int32 `json:"minimumBatches"`

	// MaximumIdleFraction bounds the fraction of the window the instance waited
	// for its next request after a batch ended.
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]+)?|1(\.0+)?)$`
	MaximumIdleFraction string `json:"maximumIdleFraction"`

	// Workers is the number of concurrent load generator senders.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4096
	Workers int32 `json:"workers"`

	// SupplyMode selects fixed outstanding supply without changing E5 arrivals.
	// +optional
	// +kubebuilder:validation:Enum=closed-loop;poisson
	SupplyMode string `json:"supplyMode,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	RequestLifetimeSeconds int32 `json:"requestLifetimeSeconds,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	DeliveryMarginMillis int32 `json:"deliveryMarginMillis,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	DrainSeconds int32 `json:"drainSeconds,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	PreparationSeconds int32 `json:"preparationSeconds,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	JobSetupSeconds int32 `json:"jobSetupSeconds,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	CollectionSeconds int32 `json:"collectionSeconds,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=3600
	PlannedElapsedSeconds int32 `json:"plannedElapsedSeconds,omitempty"`
	// Conditions freezes the calculation inputs, coefficients and identities.
	// +optional
	Conditions DispatchOccupancyConditions `json:"conditions,omitempty"`
}

// DispatchOccupancyConditions records the approved empirical planning inputs.
// InputsJSON is an object containing the original profile populations and
// source identities, not a claim of achieved precision or saturation.
type DispatchOccupancyConditions struct {
	ProfileID                  string `json:"profileID,omitempty"`
	InputsJSON                 string `json:"inputsJSON,omitempty"`
	WaitingBatches             int32  `json:"waitingBatches,omitempty"`
	MaximumIdleNanosPerRequest int64  `json:"maximumIdleNanosPerRequest,omitempty"`
	MinimumCycles              int32  `json:"minimumCycles,omitempty"`
}

// ValidateConditions checks the frozen measurement contract at admission.
func (s DispatchOccupancyMeasurementSpec) ValidateConditions() error {
	idle, err := strconv.ParseFloat(s.MaximumIdleFraction, 64)
	if err != nil || math.IsNaN(idle) || math.IsInf(idle, 0) || idle < 0 || idle > 1 ||
		s.Batch < 1 || s.Workers < 1 || s.Workers > 4096 || s.MinimumBatches < 2 || s.DurationSeconds < 60 || s.WarmupSeconds < 0 {
		return fmt.Errorf("invalid occupancy window, population or idle bound")
	}
	if s.SupplyMode != "closed-loop" {
		factor, err := strconv.ParseFloat(s.RateFactor, 64)
		if s.SupplyMode != "" && s.SupplyMode != "poisson" || err != nil || math.IsNaN(factor) || math.IsInf(factor, 0) || factor < 1.2 {
			return fmt.Errorf("invalid open-loop supply")
		}
		return nil
	}
	c := s.Conditions
	if c.InputsJSON == "" || c.ProfileID == "" ||
		c.WaitingBatches < 1 || c.MinimumCycles != s.MinimumBatches-1 || c.MaximumIdleNanosPerRequest <= 0 ||
		int64(s.Workers) != int64(s.Batch)*(1+int64(c.WaitingBatches)) {
		return fmt.Errorf("invalid frozen occupancy conditions")
	}
	return s.validateSupplyBounds()
}

func (s DispatchOccupancyMeasurementSpec) validateSupplyBounds() error {
	c := s.Conditions
	var formula struct {
		Computed struct {
			TailBatchSeconds               float64 `json:"tailBatchSeconds"`
			FastBatchSeconds               float64 `json:"fastBatchSeconds"`
			PrefixSeconds                  float64 `json:"prefixSeconds"`
			RefillSeconds                  float64 `json:"refillSeconds"`
			FramedDecodedRequestUpperBytes int64   `json:"framedDecodedRequestUpperBytes"`
			QueueRequestCap                int64   `json:"queueRequestCap"`
			QueueBytesCap                  int64   `json:"queueBytesCap"`
			QUpper                         int64   `json:"qUpper"`
		} `json:"computed"`
	}
	if err := json.Unmarshal([]byte(c.InputsJSON), &formula); err != nil {
		return fmt.Errorf("invalid frozen numeric inputs: %w", err)
	}
	f := formula.Computed
	for _, value := range []float64{f.TailBatchSeconds, f.FastBatchSeconds, f.PrefixSeconds, f.RefillSeconds} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("invalid frozen supply timing")
		}
	}
	q := int64(c.WaitingBatches)
	waiting := int64(s.Batch) * q
	if f.TailBatchSeconds <= 0 || f.FastBatchSeconds <= 0 || f.FramedDecodedRequestUpperBytes <= 0 ||
		f.QueueRequestCap < waiting || f.QueueBytesCap/f.FramedDecodedRequestUpperBytes < waiting ||
		q > f.QUpper || float64(q) != math.Max(1, math.Ceil(f.RefillSeconds/f.FastBatchSeconds)) ||
		(float64(q)+2)*f.TailBatchSeconds+f.PrefixSeconds >= float64(s.RequestLifetimeSeconds) ||
		float64(s.WarmupSeconds) < math.Ceil(math.Max(60, (float64(q)+1)*f.TailBatchSeconds+f.RefillSeconds)) ||
		float64(s.DrainSeconds) < math.Ceil(float64(s.RequestLifetimeSeconds)+f.TailBatchSeconds+float64(s.DeliveryMarginMillis)/1000) {
		return fmt.Errorf("fixed outstanding supply violates frozen count, bytes, lifetime or drain bounds")
	}
	return s.validateElapsedBudget()
}

func (s DispatchOccupancyMeasurementSpec) validateElapsedBudget() error {
	budget := int64(s.PreparationSeconds) + int64(s.JobSetupSeconds) + int64(s.CollectionSeconds) + int64(s.WarmupSeconds) + int64(s.DurationSeconds) + int64(s.DrainSeconds)
	if s.RequestLifetimeSeconds < 1 || s.DeliveryMarginMillis <= 0 || s.DrainSeconds < s.RequestLifetimeSeconds ||
		s.PreparationSeconds < 1 || s.JobSetupSeconds < 0 || s.CollectionSeconds < 0 || s.WarmupSeconds < 60 || s.DurationSeconds < 360 ||
		s.PlannedElapsedSeconds < 1 || s.PlannedElapsedSeconds > 3600 || budget > int64(s.PlannedElapsedSeconds) {
		return fmt.Errorf("invalid occupancy lifetime or elapsed budget")
	}
	return nil
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
