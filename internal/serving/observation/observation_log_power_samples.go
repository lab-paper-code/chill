package observation

import (
	"context"
	"log/slog"
	"reflect"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	serving "github.com/lab-paper-code/chill/internal/serving"
)

// MeterSamples are one meter's samples and failed reads in a window exactly as
// the scheduler stream recorded them, ordered by observation time. Coverage
// and GapUntil report stream history the read could not see, as
// OperatingPower does; no sample is interpolated, filled or resampled.
type MeterSamples struct {
	Samples  []metricobserver.Sample     `json:"samples"`
	Failures []metricobserver.Failure    `json:"failures"`
	Coverage serving.ObservationCoverage `json:"coverage"`
	GapUntil *time.Time                  `json:"gapUntil,omitempty"`
}

// PowerSamples reads the samples of one configured meter observed in
// [from, to]. Identical repeats count once. Conflicting values at one timestamp
// remain raw facts so integration excludes only windows that contain them.
// A source mismatch makes this meter's power unavailable.
func (l *ObservationLog) PowerSamples(ctx context.Context, meter OperatingPowerMeter, from, to time.Time) (MeterSamples, error) {
	result := MeterSamples{Samples: []metricobserver.Sample{}, Failures: []metricobserver.Failure{}}
	seen := map[time.Time]float64{}
	failed := map[time.Time]bool{}
	unavailable := false
	coverage, err := l.readOperatingPower(ctx, from, to, func(records func(func(*SchedulerObservation) error) error) error {
		return records(func(event *SchedulerObservation) error {
			if event.Power == nil || !matchesOperatingMeter(meter, event) {
				return nil
			}
			if len(event.Power.Samples) > 0 && !reflect.DeepEqual(event.Power.Source, meter.Source) {
				slog.Warn("meter samples came from another source", "meter", meter.Meter, "at", event.At)
				unavailable = true
				return nil
			}
			for _, sample := range event.Power.Samples {
				at := sample.ObservedAt.UTC()
				if at.Before(from) || at.After(to) {
					continue
				}
				if watts, exists := seen[at]; exists {
					if watts != sample.Watts {
						slog.Warn("conflicting meter sample", "meter", meter.Meter, "at", at, "watts", watts, "otherWatts", sample.Watts)
						result.Samples = append(result.Samples, sample)
					}
					continue
				}
				seen[at] = sample.Watts
				result.Samples = append(result.Samples, sample)
			}
			for _, failure := range event.Power.Failures {
				at := failure.ObservedAt.UTC()
				if !at.Before(from) && !at.After(to) && !failed[at] {
					failed[at] = true
					result.Failures = append(result.Failures, failure)
				}
			}
			return nil
		})
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("meter samples unavailable", "meter", meter.Meter, "reason", err)
		unavailable, err = true, nil
	}
	result.Coverage, result.GapUntil = coverage, coverage.GapUntil(from)
	if unavailable {
		result.Samples = nil
	}
	sort.Slice(result.Samples, func(i, j int) bool { return result.Samples[i].ObservedAt.Before(result.Samples[j].ObservedAt) })
	sort.Slice(result.Failures, func(i, j int) bool { return result.Failures[i].ObservedAt.Before(result.Failures[j].ObservedAt) })
	return result, err
}
