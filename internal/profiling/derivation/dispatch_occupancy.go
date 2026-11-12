package profilederivation

// DispatchOccupancyNanos returns signed dispatch occupancy per input from a
// backlogged batch pair. Callers establish pair eligibility before calling.
// Missing actual runner service is not a sample; profile means are never used.
func DispatchOccupancyNanos(cycleNanos, serviceDurationNanos int64, batchSize int) (float64, bool) {
	if cycleNanos <= 0 || serviceDurationNanos <= 0 || batchSize <= 0 {
		return 0, false
	}
	return (float64(cycleNanos) - float64(serviceDurationNanos)) / float64(batchSize), true
}
