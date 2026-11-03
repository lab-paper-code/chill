package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
)

// dispatchOccupancyPolicy is selected explicitly from the scheduler ConfigMap.
// MinimumPairs is a sampling floor, not a claim of achieved precision.
type dispatchOccupancyPolicy struct {
	AggregationWindowSeconds        int32   `json:"aggregationWindowSeconds"`
	RefreshIntervalSeconds          int32   `json:"refreshIntervalSeconds"`
	ScanTimeoutSeconds              int32   `json:"scanTimeoutSeconds"`
	MaximumRequests                 int     `json:"maximumRequests"`
	WarmupSeconds                   int32   `json:"warmupSeconds"`
	BurstSeconds                    int32   `json:"burstSeconds"`
	Workers                         int32   `json:"workers"`
	MinimumPairs                    int32   `json:"minimumPairs"`
	MaximumObservationWindowSeconds int32   `json:"maximumObservationWindowSeconds"`
	RequestLifetimeSeconds          int32   `json:"requestLifetimeSeconds"`
	DeliveryMarginMillis            int32   `json:"deliveryMarginMillis"`
	PreparationSeconds              int32   `json:"preparationSeconds"`
	JobSetupSeconds                 int32   `json:"jobSetupSeconds"`
	CollectionSeconds               int32   `json:"collectionSeconds"`
	PlannedElapsedCapSeconds        float64 `json:"plannedElapsedCapSeconds"`
	Seed                            uint64  `json:"seed"`
}

// Preserve omission separately from zero for inputs that permit zero.
func (p *dispatchOccupancyPolicy) UnmarshalJSON(raw []byte) error {
	type policy dispatchOccupancyPolicy
	var wire struct {
		policy
		WarmupSeconds     *int32  `json:"warmupSeconds"`
		JobSetupSeconds   *int32  `json:"jobSetupSeconds"`
		CollectionSeconds *int32  `json:"collectionSeconds"`
		Seed              *uint64 `json:"seed"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.WarmupSeconds == nil || wire.JobSetupSeconds == nil ||
		wire.CollectionSeconds == nil || wire.Seed == nil {
		return errors.New("dispatchOccupancy requires explicit warmup, job setup, collection and seed values; " +
			"zero is allowed")
	}
	*p = dispatchOccupancyPolicy(wire.policy)
	p.WarmupSeconds = *wire.WarmupSeconds
	p.JobSetupSeconds, p.CollectionSeconds, p.Seed = *wire.JobSetupSeconds, *wire.CollectionSeconds, *wire.Seed
	return nil
}

func (p dispatchOccupancyPolicy) validate() error {
	if p.AggregationWindowSeconds < 1 ||
		p.MaximumObservationWindowSeconds < 1 || p.MaximumObservationWindowSeconds > p.AggregationWindowSeconds ||
		p.RefreshIntervalSeconds < 1 || p.ScanTimeoutSeconds < 1 ||
		2*int64(p.ScanTimeoutSeconds) > int64(p.RefreshIntervalSeconds) || p.MaximumRequests < 1 {
		return errors.New("invalid dispatchOccupancy aggregation window, refresh, scan duty or request bound")
	}
	if p.WarmupSeconds < 0 || p.BurstSeconds < 1 || p.Workers < 1 || p.Workers > 64 ||
		p.MinimumPairs < 1 || p.MaximumObservationWindowSeconds < p.BurstSeconds ||
		p.RequestLifetimeSeconds < 1 || p.DeliveryMarginMillis < 1 || p.PreparationSeconds < 1 ||
		p.JobSetupSeconds < 0 || p.CollectionSeconds < 0 ||
		math.IsNaN(p.PlannedElapsedCapSeconds) || math.IsInf(p.PlannedElapsedCapSeconds, 0) ||
		p.PlannedElapsedCapSeconds <= 0 || p.PlannedElapsedCapSeconds > math.MaxInt32 {
		return errors.New("invalid dispatchOccupancy burst, sampling, observation window, supply or timing bounds")
	}
	// Reject an impossible cap before profile-dependent queue and drain timing.
	extension := int64(p.RequestLifetimeSeconds) + (int64(p.DeliveryMarginMillis)+999)/1000
	minimum := int64(p.PreparationSeconds) + int64(p.JobSetupSeconds) + int64(p.CollectionSeconds) +
		int64(p.WarmupSeconds) + int64(p.BurstSeconds) + 2*extension
	if minimum > int64(math.Floor(p.PlannedElapsedCapSeconds)) {
		return errors.New("dispatchOccupancy elapsed cap cannot hold its configured segments and supply extension")
	}
	return nil
}
