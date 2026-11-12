package profilederivation

import (
	"errors"
)

// measureOperatingPoints integrates each physical phase separately. Gaps between
// phases and the saturation/sparse populations never enter incremental energy.
func measureOperatingPoints(trial Trial) (float64, float64, float64, []PhasePower, []float64, error) {
	var joules, seconds, items float64
	power := make([]PhasePower, 0, len(trial.Phases))
	var occupancy []float64
	for _, phase := range trial.Phases {
		if phase.CompletedItems <= 0 || phase.CompletedItems%trial.Batch != 0 {
			return 0, 0, 0, nil, nil, errors.New("operating-point completed inputs must cover whole batches")
		}
		measured, err := MeasurePhasePower(phase)
		if err != nil {
			return 0, 0, 0, nil, nil, err
		}
		measured.TrialID = trial.TrialID
		power = append(power, measured)
		switch phase.Kind {
		case "utilization":
			joules += measured.WallJoules
			seconds += phase.WindowEnd.Sub(phase.WindowStart).Seconds()
			items += float64(phase.CompletedItems)
		case "saturation":
			occupancy = SaturationDispatchOccupancy(phase.NativeSamples, trial.Batch)
		case "sparse":
		default:
			return 0, 0, 0, nil, nil, errors.New("unknown operating-point phase")
		}
	}
	if items == 0 {
		return 0, 0, 0, nil, nil, errors.New("utilization population has no completed inputs")
	}
	return joules, seconds, items, power, occupancy, nil
}

// MeasurePhasePower retains whole-node energy and mean power for one load phase.
func MeasurePhasePower(phase OperatingPointPhase) (PhasePower, error) {
	energy, err := measurePower(phase.WindowStart, phase.WindowEnd, phase.Samples)
	if err != nil {
		return PhasePower{}, err
	}
	return PhasePower{ID: phase.ID, Kind: phase.Kind, WindowStart: phase.WindowStart, WindowEnd: phase.WindowEnd,
		CompletedItems: phase.CompletedItems, WallJoules: energy,
		MeanWallWatts: energy / phase.WindowEnd.Sub(phase.WindowStart).Seconds()}, nil
}

// SaturationDispatchOccupancy retains signed residuals only from physically
// consecutive full backlogged batches in one trial, in native batch-ID order.
func SaturationDispatchOccupancy(samples []NativeServiceSample, batch int) []float64 {
	var occupancy []float64
	for index := 1; index < len(samples); index++ {
		prev, next := &samples[index-1], &samples[index]
		if prev.TrialID == "" || prev.TrialID != next.TrialID || next.BatchID != prev.BatchID+1 ||
			prev.StartedAt.IsZero() || prev.FinishedAt.IsZero() || next.StartedAt.IsZero() ||
			next.LatestEnqueuedAt.IsZero() || next.LatestEnqueuedAt.After(prev.FinishedAt) ||
			next.StartedAt.Before(prev.FinishedAt) || len(next.RecordIDs) != batch || len(prev.RecordIDs) != batch {
			continue
		}
		if d, ok := DispatchOccupancyNanos(next.StartedAt.Sub(prev.StartedAt).Nanoseconds(), prev.ServiceDurationNanos, batch); ok {
			occupancy = append(occupancy, d)
		}
	}
	return occupancy
}
