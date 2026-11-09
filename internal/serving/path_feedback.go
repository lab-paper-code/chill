package serving

import (
	"sort"
	"time"

	"gonum.org/v1/gonum/stat"
)

// PathFeedback is one Workload's immutable additive-budget estimate.
type PathFeedback struct {
	WorkloadUID string  `json:"workloadUID"`
	Percentile  float64 `json:"percentile"`
	TotalNanos  float64 `json:"totalNanos"`
	PostNanos   float64 `json:"postNanos"`
	SampleCount int     `json:"sampleCount"`
	Source      string  `json:"source"`
}

// BuildPathFeedback uses samples already selected for this Workload and receipt
// window. The configured margin is used only when no finite sample remains.
func BuildPathFeedback(workloadUID string, samples []PathTimingSample, slo SLO, configuredMargin time.Duration) PathFeedback {
	feedback := PathFeedback{
		WorkloadUID: workloadUID, Percentile: slo.Percentile, SampleCount: len(samples),
		TotalNanos: float64(configuredMargin), PostNanos: float64(configuredMargin), Source: "configured",
	}
	if len(samples) == 0 {
		return feedback
	}
	feedback.Source = "empirical"
	if len(samples) == 1 {
		feedback.TotalNanos, feedback.PostNanos = samples[0].TotalNanos, samples[0].PostNanos
		return feedback
	}
	// Reuse one scalar buffer for both quantiles; never copy or reorder records.
	values := make([]float64, len(samples))
	for i := range samples {
		values[i] = samples[i].TotalNanos
	}
	sort.Float64s(values)
	feedback.TotalNanos = stat.Quantile(slo.Percentile/100, stat.Empirical, values, nil)
	for i := range samples {
		values[i] = samples[i].PostNanos
	}
	sort.Float64s(values)
	feedback.PostNanos = stat.Quantile(slo.Percentile/100, stat.Empirical, values, nil)
	return feedback
}
