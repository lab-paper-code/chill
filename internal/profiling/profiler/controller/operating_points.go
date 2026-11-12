package controller

import (
	"slices"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func ProjectOperatingPoints(p *api.MeasurementOperatingPointsSpec) *profilingrun.OperatingPointsContract {
	if p == nil {
		return nil
	}
	return &profilingrun.OperatingPointsContract{
		Utilizations: slices.Clone(p.Utilizations), SparseMeanGapMilliseconds: p.SparseMeanGapMilliseconds,
		PhaseDurationSeconds: int(p.PhaseDurationSeconds), RequestLifetimeSeconds: int(p.RequestLifetimeSeconds),
		DeliveryMarginMilliseconds: int(p.DeliveryMarginMilliseconds), FramedDecodedRequestUpperBytes: p.FramedDecodedRequestUpperBytes,
	}
}
