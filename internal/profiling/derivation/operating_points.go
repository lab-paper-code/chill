package profilederivation

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
