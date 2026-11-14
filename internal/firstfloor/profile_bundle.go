package firstfloor

import (
	"encoding/json"
	"errors"
)

// The immutable bundle uses the same nanosecond arrays as the session result.
// Trial identifiers have no consumer: the estimator pools physical trials.
// First executions without an idle anchor belong to no conditional population.
// They are omitted, not represented as a measured zero gap.
type batchProfileWire struct {
	Size               int     `json:"size"`
	ServiceNanos       []int64 `json:"serviceNanos"`
	PrecedingIdleNanos []int64 `json:"precedingIdleNanos"`
	EnergyPerItemJ     float64 `json:"energyPerItemJ"`
}

func (b BatchProfile) MarshalJSON() ([]byte, error) {
	d := b.Distribution
	if d == nil {
		var err error
		d, err = NewServiceDistribution(b.ServiceSamples)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(batchProfileWire{b.Size, d.services, d.gaps, b.EnergyPerItemJ})
}

func (b *BatchProfile) UnmarshalJSON(raw []byte) error {
	var wire batchProfileWire
	if err := decodeStrict(raw, &wire); err != nil {
		return err
	}
	if len(wire.ServiceNanos) == 0 || len(wire.ServiceNanos) != len(wire.PrecedingIdleNanos) {
		return errors.New("paired service and preceding idle arrays required")
	}
	records := make([]serviceRecord, len(wire.ServiceNanos))
	for i, service := range wire.ServiceNanos {
		gap := wire.PrecedingIdleNanos[i]
		if service <= 0 || gap < 0 {
			return errors.New("positive service and nonnegative actual idle required")
		}
		records[i] = serviceRecord{gap: gap, service: service}
	}
	*b = BatchProfile{Size: wire.Size, Distribution: hydrateServiceRecords(records, 0), EnergyPerItemJ: wire.EnergyPerItemJ}
	return nil
}
