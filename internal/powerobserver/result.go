package powerobserver

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Validate checks that a Result is a self-consistent observation record. It
// does not decide whether the observation has enough coverage for admission.
func (r Result) Validate() error {
	for name, value := range map[string]string{
		"source kind": r.Source.Kind,
		"endpoint":    r.Source.Endpoint,
		"metric":      r.Source.Metric,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("power observation %s is required without surrounding whitespace", name)
		}
	}
	if r.StartedAt.IsZero() || r.EndedAt.IsZero() || !r.EndedAt.After(r.StartedAt) {
		return errors.New("power observation window is invalid")
	}
	if err := validateSamples(r.Samples, r.StartedAt, r.EndedAt); err != nil {
		return err
	}
	if err := validateFailures(r.Failures, r.StartedAt, r.EndedAt); err != nil {
		return err
	}
	if r.Summary != summarize(r) {
		return errors.New("power observation summary does not match raw attempts")
	}
	return nil
}

func validateSamples(samples []Sample, started, ended time.Time) error {
	var previous time.Time
	for _, sample := range samples {
		if !withinWindow(sample.ObservedAt, started, ended) ||
			(!previous.IsZero() && sample.ObservedAt.Before(previous)) {
			return errors.New("power samples are outside the observation window or out of order")
		}
		if invalidNonNegative(sample.Watts) || invalidNonNegative(sample.RequestLatencySeconds) {
			return errors.New("power sample values must be finite and non-negative")
		}
		previous = sample.ObservedAt
	}
	return nil
}

func validateFailures(failures []Failure, started, ended time.Time) error {
	var previous time.Time
	for _, failure := range failures {
		if !withinWindow(failure.ObservedAt, started, ended) ||
			(!previous.IsZero() && failure.ObservedAt.Before(previous)) {
			return errors.New("power failures are outside the observation window or out of order")
		}
		if invalidNonNegative(failure.RequestLatencySeconds) || strings.TrimSpace(failure.Message) == "" {
			return errors.New("power failure latency and message are invalid")
		}
		switch failure.Reason {
		case FailureReasonTimeout, FailureReasonMetricMissing, FailureReasonInvalidReading, FailureReasonSourceRead:
		default:
			return fmt.Errorf("unsupported power failure reason %q", failure.Reason)
		}
		previous = failure.ObservedAt
	}
	return nil
}

func withinWindow(value, started, ended time.Time) bool {
	return !value.IsZero() && !value.Before(started) && !value.After(ended)
}

func invalidNonNegative(value float64) bool {
	return value < 0 || math.IsNaN(value) || math.IsInf(value, 0)
}
