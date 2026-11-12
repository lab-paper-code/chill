package profilingrun

import (
	"errors"
	"math"
)

// Validate checks explicit phase and sizing inputs before freezing a Run.
func (p *OperatingPointsContract) Validate() error {
	if p == nil || len(p.Utilizations) == 0 || p.PhaseDurationSeconds < 30 || p.PostLoadDurationSeconds < 1 ||
		p.RequestLifetimeSeconds < 1 || p.DeliveryMarginMilliseconds < 1 || p.FramedDecodedRequestUpperBytes < 1 {
		return errors.New("active measurement requires explicit operatingPoints phase and supply inputs")
	}
	if p.PostLoadDurationSeconds > int(^uint32(0)>>1) ||
		len(p.Utilizations) > (int(^uint32(0)>>1)-p.PostLoadDurationSeconds)/p.PhaseDurationSeconds-2 {
		return errors.New("operatingPoints phase duration exceeds supported seconds")
	}
	if p.FramedDecodedRequestUpperBytes > 64<<20 ||
		float64(p.DeliveryMarginMilliseconds)/1000 >= float64(p.RequestLifetimeSeconds) {
		return errors.New("operatingPoints request bytes or delivery margin exceed native supply bounds")
	}
	for _, value := range p.Utilizations {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value >= 1 {
			return errors.New("operatingPoints utilizations must be finite and in (0, 1)")
		}
	}
	if value := p.SparseMeanGapMilliseconds; math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return errors.New("operatingPoints sparse gap must be finite and positive")
	}
	return nil
}
