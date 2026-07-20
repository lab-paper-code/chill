package metricobserver

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
			return fmt.Errorf("metric observation %s is required without surrounding whitespace", name)
		}
	}
	if err := validateThermalMetricIdentity(r.Source.ThermalMetrics); err != nil {
		return err
	}
	if r.StartedAt.IsZero() || r.EndedAt.IsZero() || !r.EndedAt.After(r.StartedAt) {
		return errors.New("metric observation window is invalid")
	}
	if err := validateSamples(r.Samples, r.StartedAt, r.EndedAt); err != nil {
		return err
	}
	if err := validateFailures(r.Failures, r.StartedAt, r.EndedAt); err != nil {
		return err
	}
	if r.Summary != summarize(r) {
		return errors.New("metric observation summary does not match raw attempts")
	}
	return nil
}

func validateThermalMetricIdentity(identity *ThermalMetricIdentity) error {
	if identity == nil {
		return nil
	}
	seen := make(map[string]struct{}, 6)
	for name, value := range map[string]string{
		"observation available":       identity.ObservationAvailable,
		"active":                      identity.Active,
		"CPU core throttle events":    identity.CPUCoreThrottleEvents,
		"CPU package throttle events": identity.CPUPackageThrottleEvents,
		"latched":                     identity.Latched,
		"maximum temperature":         identity.MaximumTemperatureCelsius,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("thermal metric identity %s is required without surrounding whitespace", name)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("thermal metric identity %q is duplicated", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateSamples(samples []Sample, started, ended time.Time) error {
	var previous time.Time
	for _, sample := range samples {
		if !withinWindow(sample.ObservedAt, started, ended) ||
			(!previous.IsZero() && sample.ObservedAt.Before(previous)) {
			return errors.New("metric samples are outside the observation window or out of order")
		}
		if invalidNonNegative(sample.Watts) || invalidNonNegative(sample.RequestLatencySeconds) {
			return errors.New("metric sample values must be finite and non-negative")
		}
		if err := validateThermalReading(sample.Thermal); err != nil {
			return err
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
			return errors.New("metric failures are outside the observation window or out of order")
		}
		if invalidNonNegative(failure.RequestLatencySeconds) || strings.TrimSpace(failure.Message) == "" {
			return errors.New("metric failure latency and message are invalid")
		}
		switch failure.Reason {
		case FailureReasonTimeout, FailureReasonMetricMissing, FailureReasonInvalidReading, FailureReasonSourceRead:
		default:
			return fmt.Errorf("unsupported metric failure reason %q", failure.Reason)
		}
		previous = failure.ObservedAt
	}
	return nil
}

func validateThermalReading(reading *ThermalReading) error {
	if reading == nil {
		return nil
	}
	if reading.MaximumTemperatureCelsius != nil &&
		(math.IsNaN(*reading.MaximumTemperatureCelsius) || math.IsInf(*reading.MaximumTemperatureCelsius, 0)) {
		return errors.New("thermal temperature must be finite")
	}
	seen := make(map[string]struct{}, len(reading.Issues))
	for _, issue := range reading.Issues {
		if strings.TrimSpace(issue.Metric) == "" || strings.TrimSpace(issue.Metric) != issue.Metric ||
			strings.TrimSpace(issue.Message) == "" {
			return errors.New("thermal metric issue is invalid")
		}
		if issue.Reason != FailureReasonMetricMissing && issue.Reason != FailureReasonInvalidReading {
			return fmt.Errorf("unsupported thermal metric issue reason %q", issue.Reason)
		}
		if _, duplicate := seen[issue.Metric]; duplicate {
			return fmt.Errorf("duplicate thermal metric issue %q", issue.Metric)
		}
		seen[issue.Metric] = struct{}{}
	}
	return nil
}

func withinWindow(value, started, ended time.Time) bool {
	return !value.IsZero() && !value.Before(started) && !value.After(ended)
}

func invalidNonNegative(value float64) bool {
	return value < 0 || math.IsNaN(value) || math.IsInf(value, 0)
}
