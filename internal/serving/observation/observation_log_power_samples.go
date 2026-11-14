package observation

import (
	"context"
	"fmt"
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
// [from, to]. A sample repeated by overlapping records counts once; the same
// time with other watts, or a record from another source, is an error.
func (l *ObservationLog) PowerSamples(ctx context.Context, meter OperatingPowerMeter, from, to time.Time) (MeterSamples, error) {
	result := MeterSamples{Samples: []metricobserver.Sample{}, Failures: []metricobserver.Failure{}}
	if from.IsZero() || !from.Before(to) {
		return result, &serving.Error{Code: serving.CodeInvalidQuery, Message: "power samples require a positive from/to range"}
	}
	seen := map[time.Time]float64{}
	failed := map[time.Time]bool{}
	coverage, err := l.readOperatingPower(ctx, from, to, func(records func(func(*SchedulerObservation) error) error) error {
		return records(func(event *SchedulerObservation) error {
			if event.Power == nil || !matchesOperatingMeter(meter, event) {
				return nil
			}
			if len(event.Power.Samples) > 0 && !reflect.DeepEqual(event.Power.Source, meter.Source) {
				return fmt.Errorf("meter %s recorded samples from another source at %s", meter.Meter, event.At.Format(time.RFC3339Nano))
			}
			for _, sample := range event.Power.Samples {
				at := sample.ObservedAt.UTC()
				if at.Before(from) || at.After(to) {
					continue
				}
				if watts, exists := seen[at]; exists {
					if watts != sample.Watts {
						return fmt.Errorf("meter %s recorded %v W and %v W at %s", meter.Meter, watts, sample.Watts, at.Format(time.RFC3339Nano))
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
	result.Coverage, result.GapUntil = coverage, coverage.GapUntil(from)
	sort.Slice(result.Samples, func(i, j int) bool { return result.Samples[i].ObservedAt.Before(result.Samples[j].ObservedAt) })
	sort.Slice(result.Failures, func(i, j int) bool { return result.Failures[i].ObservedAt.Before(result.Failures[j].ObservedAt) })
	return result, err
}
