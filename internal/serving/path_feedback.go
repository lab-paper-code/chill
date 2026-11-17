package serving

import (
	"slices"
	"sort"
	"time"

	"gonum.org/v1/gonum/stat"
)

// PathFeedback sources: measured samples, or the configured margin without any.
const (
	pathFeedbackEmpirical  = "empirical"
	pathFeedbackConfigured = "configured"
)

// PathFeedback is one Workload's immutable additive-budget estimate. TOTAL and
// POST pool every path; Paths keeps each instance's own POST, the completion
// reserve of a request routed there.
type PathFeedback struct {
	WorkloadUID  string            `json:"workloadUID"`
	Percentile   float64           `json:"percentile"`
	TotalNanos   float64           `json:"totalNanos"`
	PostNanos    float64           `json:"postNanos"`
	SampleCount  int               `json:"sampleCount"`
	Source       string            `json:"source"`
	ObservedMean *PathObservedMean `json:"observedMean,omitempty"`
	Paths        []PathPost        `json:"paths,omitempty"`
}

// PathPost is one routing path's (instance's) POST at the Workload percentile.
type PathPost struct {
	Instance    string  `json:"instance"`
	PostNanos   float64 `json:"postNanos"`
	SampleCount int     `json:"sampleCount"`
}

// pathPost is the POST of a request routed to instance. Feedback without Paths
// (configured, or one path's view) applies to every path. A path without
// samples is not found: callers use the configured margin, never another
// path's tail.
func (f PathFeedback) pathPost(instance string) (PathPost, bool) {
	if len(f.Paths) == 0 {
		return PathPost{Instance: instance, PostNanos: f.PostNanos, SampleCount: f.SampleCount}, true
	}
	for _, path := range f.Paths {
		if path.Instance == instance {
			return path, true
		}
	}
	return PathPost{}, false
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
		TotalNanos: float64(configuredMargin), PostNanos: float64(configuredMargin), Source: pathFeedbackConfigured,
	}
	if len(samples) == 0 {
		return feedback
	}
	feedback.Source = pathFeedbackEmpirical
	mean := &PathObservedMean{}
	feedback.ObservedMean = mean
	// One scalar buffer serves every quantile; records are never copied or reordered.
	values := make([]float64, len(samples))
	tail := func(values []float64) float64 {
		sort.Float64s(values)
		return stat.Quantile(slo.Percentile/100, stat.Empirical, values, nil)
	}
	for i := range samples {
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
	feedback.TotalNanos = tail(values)
	for i := range samples {
		values[i] = samples[i].PostNanos
	}
	feedback.PostNanos = tail(values)
	// Each path's own POST, in first-sample order.
	// ponytail: O(paths*samples) scan; a Workload has few routed instances.
	for i := range samples {
		instance := samples[i].Instance
		if slices.ContainsFunc(feedback.Paths, func(path PathPost) bool { return path.Instance == instance }) {
			continue
		}
		values = values[:0]
		for _, sample := range samples[i:] {
			if sample.Instance == instance {
				values = append(values, sample.PostNanos)
			}
		}
		feedback.Paths = append(feedback.Paths, PathPost{Instance: instance, PostNanos: tail(values), SampleCount: len(values)})
	}
	return feedback
}
