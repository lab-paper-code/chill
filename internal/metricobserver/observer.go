package metricobserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Source returns one instantaneous typed metric reading. Implementations own
// transport and parsing, not scheduling or evidence-acceptance policy.
type Source interface {
	ReadMetrics(context.Context) (Reading, error)
	Identity() SourceIdentity
}

// Observer performs bounded polling against one source.
type Observer struct {
	source Source
	limits Limits
	now    func() time.Time
}

var errEventBudgetExceeded = errors.New("metric observation event budget exceeded")

// New returns an Observer for source.
func New(source Source, limits Limits) (*Observer, error) {
	if source == nil {
		return nil, errors.New("source is required")
	}
	return &Observer{
		source: source,
		limits: limits,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}, nil
}

// Validate checks that a request can be scheduled safely.
func (r Request) Validate() error {
	switch {
	case r.Interval <= 0:
		return errors.New("interval must be positive")
	case r.Duration <= 0:
		return errors.New("duration must be positive")
	case r.RequestTimeout <= 0:
		return errors.New("request timeout must be positive")
	case r.RequestTimeout >= r.Interval:
		return errors.New("request timeout must be shorter than interval")
	default:
		return nil
	}
}

// Observe samples immediately and then at a fixed interval until the bounded
// duration expires or ctx is cancelled. It calls onFirstSample exactly once,
// immediately after retaining the first successful sample. Cancellation
// returns the evidence collected so far. A defensive producer cap fails closed
// if scheduling ever exceeds the capacity admitted by request validation.
func (o *Observer) Observe(
	ctx context.Context,
	request Request,
	onFirstSample func(),
) (Result, error) {
	if onFirstSample == nil {
		return Result{}, errors.New("first sample callback is required")
	}
	capacity, err := RequiredCapacity(request, o.source.Identity(), o.limits, false)
	if err != nil {
		return Result{}, fmt.Errorf("admit observation request: %w", err)
	}

	observationCtx, cancel := context.WithTimeout(ctx, request.Duration)
	defer cancel()
	ticker := time.NewTicker(request.Interval)
	defer ticker.Stop()

	return o.observe(observationCtx, request.RequestTimeout, capacity.Events, ticker.C, onFirstSample)
}

// ObserveUntil samples through the producer's end signal and then retries a real
// final power read at Interval until one succeeds or the Duration watchdog ends.
// Callers keep ctx live while collecting pre/post support around their action.
func (o *Observer) ObserveUntil(
	ctx context.Context,
	request Request,
	end <-chan struct{},
	onFirstSample func(),
) (Result, error) {
	if end == nil || onFirstSample == nil {
		return Result{}, errors.New("end signal and first sample callback are required")
	}
	capacity, err := RequiredCapacity(request, o.source.Identity(), o.limits, true)
	if err != nil {
		return Result{}, fmt.Errorf("admit observation request: %w", err)
	}
	observationCtx, cancel := context.WithTimeout(ctx, request.Duration)
	defer cancel()
	ticker := time.NewTicker(request.Interval)
	defer ticker.Stop()
	result := o.start(capacity.Events)
	reported := false
	sample := func() error {
		err := o.sample(observationCtx, request.RequestTimeout, capacity.Events, &result)
		if !reported && len(result.Samples) > 0 {
			reported = true
			onFirstSample()
		}
		return err
	}
	if err := sample(); err != nil {
		return o.finish(result), err
	}
	for {
		select {
		case <-observationCtx.Done():
			return o.finish(result), fmt.Errorf("observation ended before producer completion: %w", observationCtx.Err())
		case <-end:
			before := len(result.Samples)
			for {
				if err := sample(); err != nil {
					return o.finish(result), err
				}
				if len(result.Samples) > before {
					return o.finish(result), nil
				}
				ticker.Reset(request.Interval)
				select {
				case <-observationCtx.Done():
					return o.finish(result), fmt.Errorf("final metric read did not produce a sample: %w", observationCtx.Err())
				case <-ticker.C:
				}
			}
		case <-ticker.C:
			if err := sample(); err != nil {
				return o.finish(result), err
			}
		}
	}
}

func (o *Observer) observe(
	ctx context.Context,
	requestTimeout time.Duration,
	maxEvents int,
	ticks <-chan time.Time,
	onFirstSample func(),
) (Result, error) {
	result := o.start(maxEvents)
	firstSampleReported := false
	sample := func() error {
		err := o.sample(ctx, requestTimeout, maxEvents, &result)
		if !firstSampleReported && len(result.Samples) > 0 {
			firstSampleReported = true
			onFirstSample()
		}
		return err
	}
	if err := sample(); err != nil {
		return o.finish(result), err
	}
	for {
		select {
		case <-ctx.Done():
			return o.finish(result), nil
		case _, ok := <-ticks:
			if !ok {
				return o.finish(result), nil
			}
			if err := sample(); err != nil {
				return o.finish(result), err
			}
		}
	}
}

func (o *Observer) start(events int) Result {
	// Each read can produce at most one sample and one failure. Allocate once;
	// retaining a thermal failure must never steal the power sample's slot.
	return Result{Source: o.source.Identity(), StartedAt: o.now(),
		Samples: make([]Sample, 0, (events+1)/2), Failures: make([]Failure, 0, (events+1)/2)}
}

func (o *Observer) finish(result Result) Result {
	result.EndedAt = o.now()
	result.Summary = Summarize(result)
	return result
}

func (o *Observer) sample(ctx context.Context, timeout time.Duration, maxEvents int, result *Result) error {
	if ctx.Err() != nil {
		return nil
	}
	if len(result.Samples)+len(result.Failures) >= maxEvents ||
		len(result.Samples) >= cap(result.Samples) || len(result.Failures) >= cap(result.Failures) {
		return errEventBudgetExceeded
	}
	started := o.now()
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reading, err := o.source.ReadMetrics(requestCtx)
	observedAt := o.now()
	latency := observedAt.Sub(started)
	if err != nil {
		// Expiration or caller cancellation ends the observation. It is not
		// evidence that the source itself failed.
		if ctx.Err() != nil {
			return nil
		}
		if deadline, ok := ctx.Deadline(); ok && !observedAt.Before(deadline) {
			return nil
		}
		failure, boundErr := boundedFailure(observedAt, latency, err)
		result.Failures = append(result.Failures, failure)
		return boundErr
	}
	var thermal *ThermalReading
	if reading.ThermalError != nil {
		if len(result.Samples)+len(result.Failures)+2 > maxEvents {
			return errEventBudgetExceeded
		}
		failure, boundErr := boundedFailure(observedAt, latency, reading.ThermalError)
		result.Failures = append(result.Failures, failure)
		// Preserve power even when its auxiliary diagnostic exceeds the bound.
		if boundErr != nil {
			result.Samples = append(result.Samples, Sample{ObservedAt: observedAt,
				Watts: reading.Watts, RequestLatencySeconds: latency.Seconds()})
			return boundErr
		}
	} else {
		if err := checkThermal(reading.Thermal, result.Source); err != nil {
			failure, boundErr := boundedFailure(observedAt, latency, err)
			result.Failures = append(result.Failures, failure)
			result.Samples = append(result.Samples, Sample{ObservedAt: observedAt,
				Watts: reading.Watts, RequestLatencySeconds: latency.Seconds()})
			if boundErr != nil {
				return boundErr
			}
			return err
		}
		thermal = cloneThermalReading(reading.Thermal)
	}
	result.Samples = append(result.Samples, Sample{
		ObservedAt:            observedAt,
		Watts:                 reading.Watts,
		Thermal:               thermal,
		RequestLatencySeconds: latency.Seconds(),
	})
	return nil
}

func cloneThermalReading(reading ThermalReading) *ThermalReading {
	copy := reading
	copy.ObservationAvailable = clonePointer(reading.ObservationAvailable)
	copy.Active = clonePointer(reading.Active)
	copy.CPUCoreThrottleEvents = clonePointer(reading.CPUCoreThrottleEvents)
	copy.CPUPackageThrottleEvents = clonePointer(reading.CPUPackageThrottleEvents)
	copy.Latched = clonePointer(reading.Latched)
	copy.MaximumTemperatureCelsius = clonePointer(reading.MaximumTemperatureCelsius)
	copy.Issues = append([]MetricIssue(nil), reading.Issues...)
	for i := range copy.Issues {
		copy.Issues[i].Metric = strings.Clone(copy.Issues[i].Metric)
		copy.Issues[i].Message = strings.Clone(copy.Issues[i].Message)
	}
	return &copy
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func classify(err error) FailureReason {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return FailureReasonTimeout
	case errors.Is(err, ErrMetricMissing):
		return FailureReasonMetricMissing
	case errors.Is(err, ErrInvalidReading):
		return FailureReasonInvalidReading
	default:
		return FailureReasonSourceRead
	}
}

func Summarize(result Result) Summary {
	latencies := make([]float64, 0, len(result.Samples)+len(result.Failures))
	for _, sample := range result.Samples {
		latencies = append(latencies, sample.RequestLatencySeconds)
	}
	for _, failure := range result.Failures {
		latencies = append(latencies, failure.RequestLatencySeconds)
	}
	sort.Float64s(latencies)

	var total float64
	for _, latency := range latencies {
		total += latency
	}
	var maximumGap time.Duration
	for index := 1; index < len(result.Samples); index++ {
		gap := result.Samples[index].ObservedAt.Sub(result.Samples[index-1].ObservedAt)
		if gap > maximumGap {
			maximumGap = gap
		}
	}
	var mean, p95 float64
	if len(latencies) > 0 {
		mean = total / float64(len(latencies))
		index := (95*len(latencies) + 99) / 100
		p95 = latencies[index-1]
	}
	return Summary{
		Attempts:                   len(latencies),
		SuccessfulSamples:          len(result.Samples),
		Failures:                   len(result.Failures),
		ObservationDurationSeconds: result.EndedAt.Sub(result.StartedAt).Seconds(),
		MeanRequestLatencySeconds:  mean,
		P95RequestLatencySeconds:   p95,
		MaximumSampleGapSeconds:    maximumGap.Seconds(),
		SourceTimestampAbsent:      true,
	}
}
