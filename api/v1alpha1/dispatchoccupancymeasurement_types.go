package v1alpha1

import (
	"encoding/json"
	"fmt"
	"math"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DispatchOccupancyMeasurementSpec declares a configured closed-loop burst.
// Pair counts do not imply statistical precision or stationarity.
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
	// RateFactor and legacy thresholds decode retained v1alpha1 evidence only.
	// New measurements neither populate nor consult these fields.
	// +optional
	RateFactor string `json:"rateFactor,omitempty"`
	// +optional
	MinimumBatches int32 `json:"minimumBatches,omitempty"`
	// +optional
	MaximumIdleFraction string `json:"maximumIdleFraction,omitempty"`

	// WarmupSeconds are excluded from the start of the window.
	// +kubebuilder:validation:Minimum=0
	WarmupSeconds int32 `json:"warmupSeconds"`

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	// MinimumPairs is the configured minimum number of eligible native pairs.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumPairs int32 `json:"minimumPairs,omitempty"`

	// Workers is the number of concurrent load generator senders.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4096
	Workers int32 `json:"workers"`

	// SupplyMode selects fixed outstanding supply without changing evaluation arrivals.
	// +optional
	// +kubebuilder:validation:Enum=closed-loop;poisson
	SupplyMode string `json:"supplyMode,omitempty"`
	// Seed selects the load-generator random sequence.
	// +optional
	Seed uint64 `json:"seed,omitempty"`
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
	// The elapsed cap is frozen in Conditions.InputsJSON coefficients.
	PlannedElapsedSeconds int32 `json:"plannedElapsedSeconds,omitempty"`
	// Conditions freezes the calculation inputs, coefficients and identities.
	// +optional
	Conditions DispatchOccupancyConditions `json:"conditions,omitempty"`
}

// DispatchOccupancyConditions freezes configuration and accepted profile identities.
type DispatchOccupancyConditions struct {
	ProfileID      string `json:"profileID,omitempty"`
	InputsJSON     string `json:"inputsJSON,omitempty"`
	WaitingBatches int32  `json:"waitingBatches,omitempty"`
	// Legacy v1alpha1 evidence fields, unused by burst creation.
	// +optional
	MaximumIdleNanosPerRequest int64 `json:"maximumIdleNanosPerRequest,omitempty"`
	// +optional
	MinimumCycles int32 `json:"minimumCycles,omitempty"`
}

// ValidateConditions checks the immutable burst and its resource/time budget.
func (s DispatchOccupancyMeasurementSpec) ValidateConditions() error {
	if s.Batch < 1 || s.Workers < s.Batch || s.Workers > 64 || s.MinimumPairs < 1 ||
		s.DurationSeconds < 1 || s.WarmupSeconds < 0 || s.SupplyMode != "closed-loop" {
		return fmt.Errorf("invalid occupancy burst, workers or minimum pairs")
	}
	c := s.Conditions
	if c.ProfileID == "" || c.InputsJSON == "" || c.WaitingBatches < 0 {
		return fmt.Errorf("invalid frozen occupancy conditions")
	}
	var inputs struct {
		Coefficients struct {
			WarmupSeconds            int32   `json:"warmupSeconds"`
			BurstSeconds             int32   `json:"burstSeconds"`
			Workers                  int32   `json:"workers"`
			MinimumPairs             int32   `json:"minimumPairs"`
			PlannedElapsedCapSeconds float64 `json:"plannedElapsedCapSeconds"`
		} `json:"coefficients"`
		Computed struct {
			TailBatchSeconds               float64 `json:"tailBatchSeconds"`
			FramedDecodedRequestUpperBytes int64   `json:"framedDecodedRequestUpperBytes"`
			QueueRequestCap                int64   `json:"queueRequestCap"`
			QueueBytesCap                  int64   `json:"queueBytesCap"`
		} `json:"computed"`
	}
	if err := json.Unmarshal([]byte(c.InputsJSON), &inputs); err != nil {
		return fmt.Errorf("invalid frozen inputs: %w", err)
	}
	p, f := inputs.Coefficients, inputs.Computed
	if p.WarmupSeconds != s.WarmupSeconds || p.BurstSeconds != s.DurationSeconds ||
		p.Workers != s.Workers || p.MinimumPairs != s.MinimumPairs ||
		p.PlannedElapsedCapSeconds <= 0 || math.IsNaN(p.PlannedElapsedCapSeconds) || math.IsInf(p.PlannedElapsedCapSeconds, 0) ||
		f.TailBatchSeconds <= 0 || math.IsNaN(f.TailBatchSeconds) || math.IsInf(f.TailBatchSeconds, 0) {
		return fmt.Errorf("burst disagrees with frozen configuration")
	}
	q := int64(math.Ceil(float64(s.Workers)/float64(s.Batch))) - 1
	waiting := int64(s.Batch) * q
	if int64(c.WaitingBatches) != q || f.FramedDecodedRequestUpperBytes <= 0 ||
		f.QueueRequestCap < waiting || f.QueueBytesCap <= 0 || f.QueueBytesCap > 64<<20 ||
		f.QueueBytesCap/f.FramedDecodedRequestUpperBytes < int64(s.Workers) ||
		(float64(q)+1)*f.TailBatchSeconds+float64(s.DeliveryMarginMillis)/1000 > float64(s.RequestLifetimeSeconds) ||
		float64(s.DrainSeconds) < math.Ceil(float64(s.RequestLifetimeSeconds)+f.TailBatchSeconds+float64(s.DeliveryMarginMillis)/1000) {
		return fmt.Errorf("configured workers exceed queue, lifetime or drain budget")
	}
	return s.validateElapsedBudget(p.PlannedElapsedCapSeconds)
}

func (s DispatchOccupancyMeasurementSpec) validateElapsedBudget(capSeconds float64) error {
	extension := int64(s.RequestLifetimeSeconds) + (int64(s.DeliveryMarginMillis)+999)/1000
	budget := int64(s.PreparationSeconds) + int64(s.JobSetupSeconds) + int64(s.CollectionSeconds) +
		int64(s.WarmupSeconds) + int64(s.DurationSeconds) + extension + int64(s.DrainSeconds)
	if s.RequestLifetimeSeconds < 1 || s.DeliveryMarginMillis <= 0 || s.PreparationSeconds < 1 ||
		s.JobSetupSeconds < 0 || s.CollectionSeconds < 0 || s.PlannedElapsedSeconds < 1 ||
		float64(s.PlannedElapsedSeconds) > capSeconds || budget > int64(s.PlannedElapsedSeconds) {
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
