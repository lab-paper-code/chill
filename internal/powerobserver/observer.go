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
	default:
		return nil
	}
}

// Observe samples immediately and then at a fixed interval until the bounded
// duration expires or ctx is cancelled. Cancellation returns the evidence
// collected so far; request validation is the only pre-run error.
func (o *Observer) Observe(ctx context.Context, request Request) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, fmt.Errorf("validate observation request: %w", err)
	}

	observationCtx, cancel := context.WithTimeout(ctx, request.Duration)
	defer cancel()
	ticker := time.NewTicker(request.Interval)
	defer ticker.Stop()

	return o.observe(observationCtx, request.RequestTimeout, ticker.C), nil
}

func (o *Observer) observe(ctx context.Context, requestTimeout time.Duration, ticks <-chan time.Time) Result {
	result := Result{Source: o.source.Identity(), StartedAt: o.now()}
	o.sample(ctx, requestTimeout, &result)
	for {
		select {
		case <-ctx.Done():
			return o.finish(result)
		case _, ok := <-ticks:
			if !ok {
				return o.finish(result)
			}
			o.sample(ctx, requestTimeout, &result)
		}
	}
}

func (o *Observer) finish(result Result) Result {
	result.EndedAt = o.now()
	result.Summary = summarize(result)
	return result
}

func (o *Observer) sample(ctx context.Context, timeout time.Duration, result *Result) {
	if ctx.Err() != nil {
		return
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
			return
		}
		if deadline, ok := ctx.Deadline(); ok && !observedAt.Before(deadline) {
			return
		}
		result.Failures = append(result.Failures, Failure{
			ObservedAt:            observedAt,
			RequestLatencySeconds: latency.Seconds(),
			Reason:                classify(err),
			Message:               err.Error(),
		})
		return
	}
	result.Samples = append(result.Samples, Sample{
		ObservedAt:            observedAt,
		Watts:                 watts,
		RequestLatencySeconds: latency.Seconds(),
	})
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
