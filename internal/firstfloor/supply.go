package firstfloor

import (
	"errors"
	"math"
)

// SaturationWorkers applies the named closed-loop sizing formula, then rejects
// supplies that cannot fit a full batch and the HTTP admission capacity.
func SaturationWorkers(batch, maxQueueRequests int, maxQueueBytes, requestBytes int64, lifetimeSeconds, marginMilliseconds, maxBatchServiceSeconds float64) (int, string, error) {
	if err := ValidateClosedLoopSupply(batch, batch, maxQueueRequests, maxQueueBytes, requestBytes, lifetimeSeconds, marginMilliseconds, maxBatchServiceSeconds); err != nil {
		return 0, "", err
	}
	bytesCap := min(maxQueueBytes, int64(64<<20))
	lifetimeCap := math.Floor((lifetimeSeconds - marginMilliseconds/1000) / (float64(batch) * maxBatchServiceSeconds))
	workers, bound := int64(64), "queue-cap"
	if byteCap := bytesCap / requestBytes; byteCap < workers {
		workers, bound = byteCap, "bytes-cap"
	}
	if lifetimeCap < float64(workers) {
		if lifetimeCap < 1 {
			return 0, "", errors.New("native supply request lifetime cannot hold one sender")
		}
		workers, bound = int64(lifetimeCap), "lifetime"
	}
	if err := ValidateClosedLoopSupply(batch, int(workers), maxQueueRequests, bytesCap, requestBytes, lifetimeSeconds, marginMilliseconds, maxBatchServiceSeconds); err != nil {
		return 0, "", err
	}
	return int(workers), bound, nil
}

// ValidateClosedLoopSupply checks configured outstanding supply without
// changing the chosen worker count. Occupancy retains its configured policy.
func ValidateClosedLoopSupply(batch, workers, maxQueueRequests int, maxQueueBytes, requestBytes int64, lifetimeSeconds, marginMilliseconds, maxBatchServiceSeconds float64) error {
	if batch < 1 || batch > 64 || workers < batch || workers > 64 ||
		maxQueueRequests < 1 || maxQueueBytes < 1 || requestBytes < 1 ||
		math.IsNaN(lifetimeSeconds) || math.IsInf(lifetimeSeconds, 0) || lifetimeSeconds <= 0 ||
		math.IsNaN(marginMilliseconds) || math.IsInf(marginMilliseconds, 0) || marginMilliseconds <= 0 ||
		math.IsNaN(maxBatchServiceSeconds) || math.IsInf(maxBatchServiceSeconds, 0) || maxBatchServiceSeconds <= 0 {
		return errors.New("invalid native closed-loop supply bounds")
	}
	bytesCap := min(maxQueueBytes, int64(64<<20))
	batches := (workers + batch - 1) / batch
	waiting := int64(batch) * int64(batches-1)
	// HTTP admission slots remain held until responses complete.
	if workers > maxQueueRequests || int64(workers) > bytesCap/requestBytes ||
		waiting > bytesCap/requestBytes || float64(batches)*maxBatchServiceSeconds+marginMilliseconds/1000 > lifetimeSeconds {
		return errors.New("configured native supply exceeds queue, byte or lifetime bounds")
	}
	return nil
}
