package application

import (
	"context"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

// OccupancyWindowError reports the accepted policy's query limit.
type OccupancyWindowError struct {
	MaximumWindowSeconds int32
}

func (*OccupancyWindowError) Error() string {
	return "occupancy window exceeds dispatchOccupancy.aggregationWindowSeconds"
}
func (o *SchedulerService) OccupancyObservations(
	ctx context.Context, from, to time.Time,
) ([]serving.OccupancyEstimate, error) {
	if o == nil {
		return nil, &serving.Error{Code: "ObservationUnavailable", Message: "scheduler is unavailable"}
	}
	config := o.configuration.Load()
	if config == nil || config.DispatchOccupancy == nil {
		return nil, &serving.Error{Code: "ObservationUnavailable", Message: "dispatchOccupancy configuration is unavailable"}
	}
	if to.Sub(from) > time.Duration(config.DispatchOccupancy.AggregationWindowSeconds)*time.Second {
		return nil, &OccupancyWindowError{
			MaximumWindowSeconds: config.DispatchOccupancy.AggregationWindowSeconds,
		}
	}
	policies := make(map[string]firstfloor.Metadata)
	for _, instance := range o.execution.Instances() {
		if instance.Native != nil {
			policies[instance.ID] = instance.Native.Metadata
		}
	}
	estimator := serving.NewOccupancyEstimator(o.observer.OccupancyProfiles(), policies, from, to)
	if _, err := o.observationLog.ScanOccupancyRows(ctx, "", "", from, to, estimator.Add); err != nil {
		return nil, err
	}
	return estimator.Results(), nil
}
