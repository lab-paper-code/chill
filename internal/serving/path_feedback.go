package serving

import (
	"sort"
	"time"

	"gonum.org/v1/gonum/stat"
)

// PathFeedback is one Workload's immutable additive-budget estimate.
type PathFeedback struct {
	WorkloadUID  string            `json:"workloadUID"`
	Percentile   float64           `json:"percentile"`
	TotalNanos   float64           `json:"totalNanos"`
	PostNanos    float64           `json:"postNanos"`
	SampleCount  int               `json:"sampleCount"`
	Source       string            `json:"source"`
	ObservedMean *PathObservedMean `json:"observedMean,omitempty"`
}

// PathObservedMean describes only the joined finite timing sample population.
// It is not a percentile decomposition or a full client success/failure metric.
type PathObservedMean struct {
	ClientNanos       float64 `json:"clientNanos"`
	ServingQueueNanos float64 `json:"servingQueueNanos"`
	NativeNanos       float64 `json:"nativeNanos"`
	NonComputeNanos   float64 `json:"nonComputeNanos"`
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
	mean := &PathObservedMean{
		ClientNanos: samples[0].ClientNanos, ServingQueueNanos: samples[0].ServingQueueNanos,
		NativeNanos: samples[0].NativeNanos, NonComputeNanos: samples[0].TotalNanos,
	}
	feedback.ObservedMean = mean
	if len(samples) == 1 {
		feedback.TotalNanos, feedback.PostNanos = samples[0].TotalNanos, samples[0].PostNanos
		return feedback
	}
	// Reuse one scalar buffer for both quantiles; never copy or reorder records.
	values := make([]float64, len(samples))
	values[0] = samples[0].TotalNanos
	for i := 1; i < len(samples); i++ {
		values[i] = samples[i].TotalNanos
		mean.ClientNanos += samples[i].ClientNanos
		mean.ServingQueueNanos += samples[i].ServingQueueNanos
		mean.NativeNanos += samples[i].NativeNanos
		mean.NonComputeNanos += samples[i].TotalNanos
	}
	count := float64(len(samples))
	mean.ClientNanos /= count
	mean.ServingQueueNanos /= count
	mean.NativeNanos /= count
	mean.NonComputeNanos /= count
	sort.Float64s(values)
	feedback.TotalNanos = stat.Quantile(slo.Percentile/100, stat.Empirical, values, nil)
	for i := range samples {
		values[i] = samples[i].PostNanos
	}
	sort.Float64s(values)
	feedback.PostNanos = stat.Quantile(slo.Percentile/100, stat.Empirical, values, nil)
	return feedback
}
