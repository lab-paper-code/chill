package serving

import (
	"context"
	"fmt"
	"time"
)

// FeedbackRateTest records a completed positive-rate experiment at current input.
type FeedbackRateTest struct {
	InputKey string  `json:"inputKey"`
	Rate     float64 `json:"rate"`
	Met      bool    `json:"met"`
	Passed   int     `json:"passed"`
	Requests int     `json:"requests"`
}

// FeedbackCapacity is a lower witness over Tests, not physical throughput.
type FeedbackCapacity struct {
	KappaHat float64            `json:"kappaHat"`
	Tests    []FeedbackRateTest `json:"tests"`
}

// JudgeFeedbackRates judges only supplied positive nominal shares under current
// TOTAL/POST. Seeded Poisson gaps and the conditional sampler are unchanged.
func JudgeFeedbackRates(ctx context.Context, settings SchedulerSettings, option SchedulerOption, definition *Definition, occupancy DispatchOccupancy, feedback PathFeedback, rates []float64) (FeedbackCapacity, error) {
	if err := ctx.Err(); err != nil {
		return FeedbackCapacity{}, err
	}
	if option.Instance.Profile == nil {
		return FeedbackCapacity{}, fmt.Errorf("missing feedback rate profile: %s", option.Instance.ID)
	}
	settings.CompletionMarginMS = feedback.PostNanos / 1e6
	input := NewCapacityInput(settings, []DispatchOccupancy{occupancy}, option, definition)
	input.DispatchOccupancy = &occupancy
	replay := capacityReplay{
		settings: settings, at: time.Unix(0, 0), occupancy: occupancy,
		option: option, w: definition, feedback: &feedback, gaps: map[[2]int][]float64{},
	}
	key := fmt.Sprintf("%s/total=%g/post=%g/wait=%d", input.Key(), feedback.TotalNanos, feedback.PostNanos, option.Instance.MaxWait)
	builder := capacityBuilder{settings: settings, percentile: definition.SLO.Percentile, replay: replay.window}
	return judgeFeedbackRates(ctx, builder, key, rates)
}

// judgeFeedbackRates reuses the Poisson judge without grid search, interpolation,
// structural zero shortcuts, or an availability lifecycle.
func judgeFeedbackRates(ctx context.Context, builder capacityBuilder, inputKey string, rates []float64) (FeedbackCapacity, error) {
	out := FeedbackCapacity{Tests: []FeedbackRateTest{}}
	seen := make(map[float64]bool, len(rates))
	for _, rate := range rates {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if !schedulerFinite(rate) || rate < 0 {
			return out, fmt.Errorf("feedback rate must be finite and nonnegative")
		}
		if rate == 0 || seen[rate] {
			continue
		}
		seen[rate] = true
		judgment, err := builder.judge(ctx, rate)
		if err != nil {
			return out, err
		}
		out.Tests = append(out.Tests, FeedbackRateTest{InputKey: inputKey, Rate: rate, Met: judgment.Met, Passed: judgment.Passed, Requests: judgment.Requests})
		if judgment.Met {
			out.KappaHat = max(out.KappaHat, rate)
		}
	}
	return out, nil
}
