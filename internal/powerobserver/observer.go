package powerobserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Source returns one instantaneous power reading. Implementations own
// transport and parsing, not scheduling or evidence-acceptance policy.
type Source interface {
	ReadPower(context.Context) (watts float64, err error)
	Identity() SourceIdentity
}

// Observer performs bounded polling against one source.
type Observer struct {
	source Source
	now    func() time.Time
}

var errEventBudgetExceeded = errors.New("power observation event budget exceeded")

// New returns an Observer for source.
func New(source Source) (*Observer, error) {
	if source == nil {
		return nil, errors.New("source is required")
	}
	return &Observer{
		source: source,
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
	case r.MaxEvents < 1:
		return errors.New("maximum retained events must be positive")
	case uint64(r.Duration/r.Interval)+2 > uint64(r.MaxEvents):
		// One immediate read plus every possible ticker delivery, including a
		// delivery racing the duration deadline, must fit before work starts.
		return errors.New("maximum retained events cannot cover the observation window")
	default:
		return nil
	}
}

// Observe samples immediately and then at a fixed interval until the bounded
// duration expires or ctx is cancelled. Cancellation returns the evidence
// collected so far. A defensive producer cap fails closed if scheduling ever
// exceeds the capacity admitted by request validation.
func (o *Observer) Observe(ctx context.Context, request Request) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate observation request: %w", err)
	}

	observationCtx, cancel := context.WithTimeout(ctx, request.Duration)
	defer cancel()
	ticker := time.NewTicker(request.Interval)
	defer ticker.Stop()

	return o.observe(observationCtx, request.RequestTimeout, request.MaxEvents, ticker.C)
}

func (o *Observer) observe(
	ctx context.Context,
	requestTimeout time.Duration,
	maxEvents int,
	ticks <-chan time.Time,
) (Result, error) {
	result := Result{Source: o.source.Identity(), StartedAt: o.now()}
	if err := o.sample(ctx, requestTimeout, maxEvents, &result); err != nil {
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
			if err := o.sample(ctx, requestTimeout, maxEvents, &result); err != nil {
				return o.finish(result), err
			}
		}
	}
}

func (o *Observer) finish(result Result) Result {
	result.EndedAt = o.now()
	result.Summary = summarize(result)
	return result
}

func (o *Observer) sample(ctx context.Context, timeout time.Duration, maxEvents int, result *Result) error {
	if ctx.Err() != nil {
		return nil
	}
	if len(result.Samples)+len(result.Failures) >= maxEvents {
		return errEventBudgetExceeded
	}
	started := o.now()
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	watts, err := o.source.ReadPower(requestCtx)
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
		result.Failures = append(result.Failures, Failure{
			ObservedAt:            observedAt,
			RequestLatencySeconds: latency.Seconds(),
			Reason:                classify(err),
			Message:               err.Error(),
		})
		return nil
	}
	result.Samples = append(result.Samples, Sample{
		ObservedAt:            observedAt,
		Watts:                 watts,
		RequestLatencySeconds: latency.Seconds(),
	})
	return nil
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

func summarize(result Result) Summary {
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
