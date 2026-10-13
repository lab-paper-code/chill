// Package worker assembles one transition observation from concurrent power
// and transition-fact observation. Concrete meter, Kubernetes, and evidence
// adapters remain outside this package.
package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

// PowerObserver is satisfied by metricobserver.Observer. It reports ready
// exactly once, after retaining its first successful power sample.
type PowerObserver interface {
	Observe(context.Context, metricobserver.Request, func()) (metricobserver.Result, error)
}

// FactObserver retains transition, read-back, readiness, and process facts. It
// reports ready exactly once, after its observation path can retain those
// facts.
type FactObserver interface {
	Observe(context.Context, func()) (transitioncost.RawObservation, error)
}

// Observe starts both observation paths before reporting readiness. When the
// fact path finishes, it stops power polling and returns their one combined raw
// observation. Scientific validation remains transitioncost.Derive's job.
func Observe(
	ctx context.Context,
	power PowerObserver,
	powerRequest metricobserver.Request,
	facts FactObserver,
	onObservationReady func(),
) (transitioncost.RawObservation, error) {
	if power == nil {
		return transitioncost.RawObservation{}, errors.New("power observer is required")
	}
	if facts == nil {
		return transitioncost.RawObservation{}, errors.New("transition fact observer is required")
	}
	if onObservationReady == nil {
		return transitioncost.RawObservation{}, errors.New("observation-ready callback is required")
	}

	observeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	powerReady := make(chan struct{})
	factsReady := make(chan struct{})
	powerDone := make(chan powerOutcome, 1)
	factsDone := make(chan factsOutcome, 1)

	go func() {
		result, err := power.Observe(observeCtx, powerRequest, func() {
			select {
			case powerReady <- struct{}{}:
			case <-observeCtx.Done():
			}
		})
		powerDone <- powerOutcome{result: result, err: err}
	}()

	go func() {
		raw, err := facts.Observe(observeCtx, func() {
			select {
			case factsReady <- struct{}{}:
			case <-observeCtx.Done():
			}
		})
		factsDone <- factsOutcome{raw: raw, err: err}
	}()

	for powerReady != nil || factsReady != nil {
		select {
		case <-ctx.Done():
			cancel()
			powerResult := <-powerDone
			factResult := <-factsDone
			return combine(factResult.raw, powerResult.result), ctx.Err()
		case <-powerReady:
			powerReady = nil
		case <-factsReady:
			factsReady = nil
		case powerResult := <-powerDone:
			cancel()
			factResult := <-factsDone
			combined := combine(factResult.raw, powerResult.result)
			if powerResult.err != nil {
				return combined, fmt.Errorf(
					"power observation ended before observation became ready: %w", powerResult.err,
				)
			}
			return combined, errors.New("power observation ended before observation became ready")
		case factResult := <-factsDone:
			cancel()
			powerResult := <-powerDone
			combined := combine(factResult.raw, powerResult.result)
			if factResult.err != nil {
				return combined, fmt.Errorf(
					"transition fact observation ended before observation became ready: %w", factResult.err,
				)
			}
			return combined, errors.New("transition fact observation ended before observation became ready")
		}
	}

	if err := ctx.Err(); err != nil {
		cancel()
		powerResult := <-powerDone
		factResult := <-factsDone
		return combine(factResult.raw, powerResult.result), err
	}
	onObservationReady()
	if err := ctx.Err(); err != nil {
		cancel()
		powerResult := <-powerDone
		factResult := <-factsDone
		return combine(factResult.raw, powerResult.result), err
	}
	select {
	case <-ctx.Done():
		cancel()
		powerResult := <-powerDone
		factResult := <-factsDone
		return combine(factResult.raw, powerResult.result), ctx.Err()
	case factResult := <-factsDone:
		cancel()
		powerResult := <-powerDone
		combined := combine(factResult.raw, powerResult.result)
		if factResult.err != nil {
			return combined, fmt.Errorf("observe transition facts: %w", factResult.err)
		}
		if powerResult.err != nil {
			return combined, fmt.Errorf("observe power: %w", powerResult.err)
		}
		return combined, nil
	case powerResult := <-powerDone:
		cancel()
		factResult := <-factsDone
		combined := combine(factResult.raw, powerResult.result)
		if powerResult.err != nil {
			return combined, fmt.Errorf("power observation ended before transition facts: %w", powerResult.err)
		}
		return combined, errors.New("power observation ended before transition facts")
	}
}

type powerOutcome struct {
	result metricobserver.Result
	err    error
}

type factsOutcome struct {
	raw transitioncost.RawObservation
	err error
}

func combine(raw transitioncost.RawObservation, power metricobserver.Result) transitioncost.RawObservation {
	raw.ObservationStartedAt = earliest(raw.ObservationStartedAt, power.StartedAt)
	raw.ObservationEndedAt = latest(raw.ObservationEndedAt, power.EndedAt)
	raw.PowerSource = power.Source
	raw.PowerSamples = make([]transitioncost.PowerSample, len(power.Samples))
	for index, sample := range power.Samples {
		raw.PowerSamples[index] = transitioncost.PowerSample{
			ObservedAt:            sample.ObservedAt,
			RequestLatencySeconds: sample.RequestLatencySeconds,
			Watts:                 sample.Watts,
		}
	}
	raw.PowerFailures = make([]transitioncost.PowerReadFailure, len(power.Failures))
	for index, failure := range power.Failures {
		raw.PowerFailures[index] = transitioncost.PowerReadFailure{
			ObservedAt:            failure.ObservedAt,
			RequestLatencySeconds: failure.RequestLatencySeconds,
			Reason:                string(failure.Reason),
			Message:               failure.Message,
		}
	}
	return raw
}

func earliest(left, right time.Time) time.Time {
	if left.IsZero() || (!right.IsZero() && right.Before(left)) {
		return right
	}
	return left
}

func latest(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
