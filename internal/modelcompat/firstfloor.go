package modelcompat

import (
	"errors"
	"strings"
)

// FirstfloorSettings are selected scheduler settings shared by every
// measurement trial and serving instance of the same frozen condition.
type FirstfloorSettings struct {
	MaxBatchSize   int32  `json:"maxBatchSize"`
	MaxQueueSize   int32  `json:"maxQueueSize"`
	DeadlinePolicy string `json:"deadlinePolicy,omitempty"`
}

// ValidateRuntimeImage requires a frozen digest reference, not a mutable tag.
// Digest spelling is left to the registry; this boundary selects the pin.
func ValidateRuntimeImage(image string) error {
	repository, digest, pinned := strings.Cut(image, "@sha256:")
	if !pinned || repository == "" || digest == "" || strings.ContainsAny(image, " \t\r\n") {
		return errors.New("runtime image must be digest-pinned as repository@sha256:digest")
	}
	return nil
}

func ValidateFirstfloorRuntimeSettings(settings *FirstfloorSettings) error {
	if settings == nil || settings.MaxBatchSize < 1 || settings.MaxQueueSize < 1 {
		return errors.New("firstfloor maxBatchSize and maxQueueSize must be selected positive int32 values")
	}
	switch EffectiveDeadlinePolicy(settings.DeadlinePolicy) {
	case "drop", "keep", "defer":
	default:
		return errors.New("firstfloor deadlinePolicy must be drop, keep, or defer")
	}
	return nil
}

// EffectiveDeadlinePolicy applies the selected setting's keep default.
func EffectiveDeadlinePolicy(value string) string {
	if value == "" {
		return "keep"
	}
	return value
}

func CloneFirstfloorRuntimeSettings(settings *FirstfloorSettings) *FirstfloorSettings {
	if settings == nil {
		return nil
	}
	clone := *settings
	clone.DeadlinePolicy = EffectiveDeadlinePolicy(clone.DeadlinePolicy)
	return &clone
}
