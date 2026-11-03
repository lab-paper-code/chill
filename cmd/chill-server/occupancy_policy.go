package main

import (
	"errors"
	"math"
)

// dispatchOccupancyPolicy is selected explicitly from the scheduler ConfigMap.
// These are planning inputs, not evidence that the generator achieved saturation.
type dispatchOccupancyPolicy struct {
	SupplyMode                  string  `json:"supplyMode"`
	MinimumBatches              int32   `json:"minimumBatches"`
	MaximumIdleFraction         float64 `json:"maximumIdleFraction"`
	RequestLifetimeSeconds      int32   `json:"requestLifetimeSeconds"`
	DeliveryMarginMillis        int32   `json:"deliveryMarginMillis"`
	PreparationSeconds          int32   `json:"preparationSeconds"`
	JobSetupSeconds             int32   `json:"jobSetupSeconds"`
	CollectionSeconds           int32   `json:"collectionSeconds"`
	WindowFloorSeconds          float64 `json:"windowFloorSeconds"`
	LoadPreparationFloorSeconds float64 `json:"loadPreparationFloorSeconds"`
	PlannedElapsedCapSeconds    float64 `json:"plannedElapsedCapSeconds"`
	TimeMargin                  float64 `json:"timeMargin"`
	RelativeXPlanningPrecision  float64 `json:"relativeXPlanningPrecision"`
	PlanningConfidence          float64 `json:"planningConfidence"`
	MaximumIdleBaseFraction     float64 `json:"maximumIdleBaseFraction"`
	IdleDErrorSecondsPerRequest float64 `json:"idleDErrorSecondsPerRequest"`
	// RefillSeconds bounds generator completion-to-replacement delivery, including jitter.
	RefillSeconds float64 `json:"refillSeconds"`
	Seed          uint64  `json:"seed"`
}

func (p dispatchOccupancyPolicy) validate() error {
	for _, v := range []float64{p.WindowFloorSeconds, p.LoadPreparationFloorSeconds, p.PlannedElapsedCapSeconds,
		p.TimeMargin, p.RelativeXPlanningPrecision, p.PlanningConfidence, p.MaximumIdleBaseFraction,
		p.IdleDErrorSecondsPerRequest, p.RefillSeconds} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return errors.New("dispatchOccupancy requires explicit finite positive planning coefficients")
		}
	}
	if p.SupplyMode != "closed-loop" || p.MinimumBatches < 2 || p.RequestLifetimeSeconds < 1 ||
		p.DeliveryMarginMillis < 1 || p.PreparationSeconds < 1 || p.JobSetupSeconds < 0 || p.CollectionSeconds < 0 ||
		math.IsNaN(p.MaximumIdleFraction) || math.IsInf(p.MaximumIdleFraction, 0) || p.MaximumIdleFraction < 0 ||
		p.MaximumIdleFraction > p.MaximumIdleBaseFraction || p.MaximumIdleBaseFraction > 1 ||
		p.RelativeXPlanningPrecision >= 1 || p.PlanningConfidence >= 1 ||
		p.PlannedElapsedCapSeconds > math.MaxInt32 || p.WindowFloorSeconds > math.MaxInt32 ||
		p.LoadPreparationFloorSeconds > math.MaxInt32 || p.IdleDErrorSecondsPerRequest*1e9 < 1 ||
		p.IdleDErrorSecondsPerRequest*1e9 >= float64(math.MaxInt64) {
		return errors.New("invalid dispatchOccupancy supply, population, timing, precision or idle bounds")
	}
	// Reject a policy whose irreducible segments cannot fit even before profile timing.
	extension := int64(p.RequestLifetimeSeconds) + (int64(p.DeliveryMarginMillis)+999)/1000
	minimum := int64(p.PreparationSeconds) + int64(p.JobSetupSeconds) + int64(p.CollectionSeconds) + extension +
		int64(p.RequestLifetimeSeconds) + int64(math.Ceil(p.WindowFloorSeconds)) + int64(math.Ceil(p.LoadPreparationFloorSeconds))
	if minimum > int64(math.Floor(p.PlannedElapsedCapSeconds)) {
		return errors.New("dispatchOccupancy elapsed cap cannot hold its configured segments and supply extension")
	}
	return nil
}
