package modelcompat

import "errors"

// TritonSettings are selected native scheduler settings shared by every
// measurement trial and serving instance of the same frozen condition.
type TritonSettings struct {
	MaxBatchSize   int32 `json:"maxBatchSize"`
	PriorityLevels int32 `json:"priorityLevels"`
	MaxQueueSize   int32 `json:"maxQueueSize"`
}

func ValidateTritonRuntimeSettings(settings *TritonSettings) error {
	if settings == nil || settings.MaxBatchSize < 1 || settings.PriorityLevels < 1 || settings.MaxQueueSize < 1 {
		return errors.New("Triton maxBatchSize, priorityLevels, and maxQueueSize must be selected positive int32 values")
	}
	return nil
}

func CloneTritonRuntimeSettings(settings *TritonSettings) *TritonSettings {
	if settings == nil {
		return nil
	}
	clone := *settings
	return &clone
}
