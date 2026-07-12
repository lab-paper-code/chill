package powerobserver

import "errors"

var (
	// ErrMetricMissing indicates that a source response did not contain its
	// configured power metric.
	ErrMetricMissing = errors.New("power metric missing")
	// ErrInvalidReading indicates that a source returned an unusable power
	// value or response representation.
	ErrInvalidReading = errors.New("invalid power reading")
)
