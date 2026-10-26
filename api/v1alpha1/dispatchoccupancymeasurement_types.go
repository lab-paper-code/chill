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
// +kubebuilder:validation:XValidation:rule="(has(self.supplyMode) && self.supplyMode == 'closed-loop') || self.durationSeconds >= 60",message="open-loop occupancy duration must be at least 60 seconds"
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

	// +kubebuilder:validation:Minimum=1
	DurationSeconds int32 `json:"durationSeconds"`

	// MinimumBatches is the least number of batches the window must hold.
	// +kubebuilder:validation:Minimum=2
	MinimumBatches int32 `json:"minimumBatches"`

	// MaximumIdleFraction bounds identified supply idle as a fraction of the window.
	// This configurable policy is range checked in [0,1]; the approved planning
	// input is 0.05. Measured d remains conservative, without idle subtraction.
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
	// The elapsed cap is frozen in Conditions.InputsJSON coefficients.
	PlannedElapsedSeconds int32 `json:"plannedElapsedSeconds,omitempty"`
	// Conditions freezes the calculation inputs, coefficients and identities.
	// +optional
	Conditions DispatchOccupancyConditions `json:"conditions,omitempty"`
}

// DispatchOccupancyConditions records the approved empirical planning inputs.
// InputsJSON is an object containing the original profile populations and
// source identities, not a claim of achieved precision or saturation. Optional
// computed.supplySizing freezes the batch, observed maximum batch service time
// and its profile source, request lifetime, delivery margin, worker/byte caps and limiting bound.
type DispatchOccupancyConditions struct {
	ProfileID                  string `json:"profileID,omitempty"`
	InputsJSON                 string `json:"inputsJSON,omitempty"`
	WaitingBatches             int32  `json:"waitingBatches,omitempty"`
	MaximumIdleNanosPerRequest int64  `json:"maximumIdleNanosPerRequest,omitempty"`
	MinimumCycles              int32  `json:"minimumCycles,omitempty"`
}

// OccupancyPlanningFloors projects the configured floors retained in the
// immutable derivation JSON. These are planning inputs, not CRD defaults.
type OccupancyPlanningFloors struct {
	WindowFloorSeconds          float64 `json:"windowFloorSeconds"`
	LoadPreparationFloorSeconds float64 `json:"loadPreparationFloorSeconds"`
	PlannedElapsedCapSeconds    float64 `json:"plannedElapsedCapSeconds"`
}

// ValidateConditions checks the frozen measurement contract at admission.
func (s DispatchOccupancyMeasurementSpec) ValidateConditions() error {
	idle, err := strconv.ParseFloat(s.MaximumIdleFraction, 64)
	if err != nil || math.IsNaN(idle) || math.IsInf(idle, 0) || idle < 0 || idle > 1 ||
		s.Batch < 1 || s.Workers < 1 || s.Workers > 4096 || s.MinimumBatches < 2 || s.DurationSeconds < 1 || s.WarmupSeconds < 0 {
		return fmt.Errorf("invalid occupancy window, population or idle bound")
	}
	if s.SupplyMode != "closed-loop" {
		// Legacy open-loop windows retain their independent structural floor.
		if s.DurationSeconds < 60 {
			return fmt.Errorf("legacy open-loop occupancy duration must be at least 60 seconds")
		}
		factor, err := strconv.ParseFloat(s.RateFactor, 64)
		if s.SupplyMode != "" && s.SupplyMode != "poisson" || err != nil || math.IsNaN(factor) || math.IsInf(factor, 0) || factor < 1.2 {
			return fmt.Errorf("invalid open-loop supply")
		}
		return nil
	}
	c := s.Conditions
	if c.InputsJSON == "" || c.ProfileID == "" ||
		c.WaitingBatches < 0 || c.MinimumCycles != s.MinimumBatches-1 || c.MaximumIdleNanosPerRequest <= 0 {
		return fmt.Errorf("invalid frozen occupancy conditions")
	}
	return s.validateSupplyBounds()
}

type occupancySupplySizing struct {
	Batch                          int32   `json:"batch"`
	ProfileMaxBatchSeconds         float64 `json:"profileMaxBatchSeconds"`
	ServiceTimeSource              string  `json:"serviceTimeSource"`
	RequestLifetimeSeconds         float64 `json:"requestLifetimeSeconds"`
	MarginSeconds                  float64 `json:"marginSeconds"`
	WorkerCap                      int32   `json:"workerCap"`
	QueueBytesCap                  int64   `json:"queueBytesCap"`
	FramedDecodedRequestUpperBytes int64   `json:"framedDecodedRequestUpperBytes"`
	LimitingBound                  string  `json:"limitingBound"`
}

func (sizing occupancySupplySizing) workers(s DispatchOccupancyMeasurementSpec, profileBatch int32, profileMax float64, queueBytes, requestBytes int64) (float64, error) {
	if sizing.Batch != s.Batch || profileBatch != s.Batch ||
		sizing.ProfileMaxBatchSeconds <= 0 || math.IsNaN(sizing.ProfileMaxBatchSeconds) || math.IsInf(sizing.ProfileMaxBatchSeconds, 0) ||
		sizing.ProfileMaxBatchSeconds != profileMax || sizing.ServiceTimeSource != "profile.maxObservedSeconds" ||
		sizing.RequestLifetimeSeconds != float64(s.RequestLifetimeSeconds) ||
		sizing.MarginSeconds != float64(s.DeliveryMarginMillis)/1000 || sizing.WorkerCap != 64 ||
		sizing.QueueBytesCap != 67108864 || sizing.QueueBytesCap != queueBytes ||
		sizing.FramedDecodedRequestUpperBytes <= 0 || sizing.FramedDecodedRequestUpperBytes != requestBytes {
		return 0, fmt.Errorf("invalid frozen supply sizing inputs")
	}
	workers, bound := float64(sizing.WorkerCap), "queue-cap"
	if bytesWorkers := float64(sizing.QueueBytesCap / sizing.FramedDecodedRequestUpperBytes); bytesWorkers < workers {
		workers, bound = bytesWorkers, "bytes-cap"
	}
	if lifetimeWorkers := math.Floor((sizing.RequestLifetimeSeconds - sizing.MarginSeconds) / (float64(s.Batch) * sizing.ProfileMaxBatchSeconds)); lifetimeWorkers < workers {
		workers, bound = lifetimeWorkers, "lifetime"
	}
	if workers < float64(s.Batch) || sizing.LimitingBound != bound {
		return 0, fmt.Errorf("infeasible supply or inconsistent frozen limiting bound")
	}
	if float64(s.Workers) != workers {
		return 0, fmt.Errorf("fixed outstanding supply disagrees with frozen supply sizing formula")
	}
	return workers, nil
}

func (s DispatchOccupancyMeasurementSpec) validateSupplyBounds() error {
	c := s.Conditions
	var formula struct {
		Coefficients OccupancyPlanningFloors `json:"coefficients"`
		Profile      struct {
			Batch              int32   `json:"batch"`
			MaxObservedSeconds float64 `json:"maxObservedSeconds"`
		} `json:"profile"`
		Computed struct {
			TailBatchSeconds               float64                `json:"tailBatchSeconds"`
			FastBatchSeconds               float64                `json:"fastBatchSeconds"`
			PrefixSeconds                  float64                `json:"prefixSeconds"`
			RefillSeconds                  float64                `json:"refillSeconds"`
			FramedDecodedRequestUpperBytes int64                  `json:"framedDecodedRequestUpperBytes"`
			QueueRequestCap                int64                  `json:"queueRequestCap"`
			QueueBytesCap                  int64                  `json:"queueBytesCap"`
			QUpper                         int64                  `json:"qUpper"`
			SupplySizing                   *occupancySupplySizing `json:"supplySizing"`
		} `json:"computed"`
	}
	if err := json.Unmarshal([]byte(c.InputsJSON), &formula); err != nil {
		return fmt.Errorf("invalid frozen numeric inputs: %w", err)
	}
	f := formula.Computed
	floors := formula.Coefficients
	for _, value := range []float64{floors.WindowFloorSeconds, floors.LoadPreparationFloorSeconds, floors.PlannedElapsedCapSeconds} {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("invalid frozen planning floors")
		}
	}
	for _, value := range []float64{f.TailBatchSeconds, f.FastBatchSeconds, f.PrefixSeconds, f.RefillSeconds} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("invalid frozen supply timing")
		}
	}
	q := int64(c.WaitingBatches)
	waiting := int64(s.Batch) * q
	expectedQ := math.Max(1, math.Ceil(f.RefillSeconds/f.FastBatchSeconds))
	lifetimeInvalid := (float64(q)+2)*f.TailBatchSeconds+f.PrefixSeconds >= float64(s.RequestLifetimeSeconds)
	if sizing := f.SupplySizing; sizing != nil {
		workers, err := sizing.workers(s, formula.Profile.Batch, formula.Profile.MaxObservedSeconds, f.QueueBytesCap, f.FramedDecodedRequestUpperBytes)
		if err != nil {
			return err
		}
		expectedQ = math.Ceil(workers/float64(s.Batch)) - 1
		lifetimeInvalid = workers*float64(s.Batch)*sizing.ProfileMaxBatchSeconds+sizing.MarginSeconds > sizing.RequestLifetimeSeconds
	} else if c.WaitingBatches < 1 || int64(s.Workers) != int64(s.Batch)*(1+q) {
		return fmt.Errorf("invalid legacy frozen occupancy supply")
	}
	if f.TailBatchSeconds <= 0 || f.FastBatchSeconds <= 0 || f.FramedDecodedRequestUpperBytes <= 0 ||
		f.QueueRequestCap < waiting || f.QueueBytesCap/f.FramedDecodedRequestUpperBytes < waiting ||
		q > f.QUpper || float64(q) != expectedQ ||
		lifetimeInvalid ||
		float64(s.WarmupSeconds) < math.Ceil(math.Max(floors.LoadPreparationFloorSeconds, (float64(q)+1)*f.TailBatchSeconds+f.RefillSeconds)) ||
		float64(s.DrainSeconds) < math.Ceil(float64(s.RequestLifetimeSeconds)+f.TailBatchSeconds+float64(s.DeliveryMarginMillis)/1000) {
		return fmt.Errorf("fixed outstanding supply violates frozen count, bytes, lifetime or drain bounds")
	}
	return s.validateElapsedBudget(floors)
}

func (s DispatchOccupancyMeasurementSpec) validateElapsedBudget(floors OccupancyPlanningFloors) error {
	supplyExtension := int64(s.RequestLifetimeSeconds) + (int64(s.DeliveryMarginMillis)+999)/1000
	budget := int64(s.PreparationSeconds) + int64(s.JobSetupSeconds) + int64(s.CollectionSeconds) + int64(s.WarmupSeconds) + int64(s.DurationSeconds) + supplyExtension + int64(s.DrainSeconds)
	if s.RequestLifetimeSeconds < 1 || s.DeliveryMarginMillis <= 0 || s.DrainSeconds < s.RequestLifetimeSeconds ||
		s.PreparationSeconds < 1 || s.JobSetupSeconds < 0 || s.CollectionSeconds < 0 ||
		float64(s.WarmupSeconds) < math.Ceil(floors.LoadPreparationFloorSeconds) || float64(s.DurationSeconds) < math.Ceil(floors.WindowFloorSeconds) ||
		s.PlannedElapsedSeconds < 1 || float64(s.PlannedElapsedSeconds) > floors.PlannedElapsedCapSeconds || budget > int64(s.PlannedElapsedSeconds) {
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
