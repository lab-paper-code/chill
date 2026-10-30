package profilederivation

import (
	"errors"
	"math"
	"sort"

	"gonum.org/v1/gonum/stat"
	"gonum.org/v1/gonum/stat/distuv"
)

// BatchStatistics projects an already-derived point without changing its estimator.
type BatchStatistics struct {
	Batch                        int             `json:"batch"`
	IncrementalWorkJoulesPerItem float64         `json:"incrementalWorkJoulesPerItem"`
	ActiveTrialCount             int             `json:"activeTrialCount"`
	IdleTrialCount               int             `json:"idleTrialCount"`
	Confidence                   PointConfidence `json:"confidence"`
	LatencySampleCount           int             `json:"latencySampleCount"`
	LatencyP95Milliseconds       float64         `json:"latencyP95Milliseconds"`
}

// Statistics copies point estimates and pools the client call latency samples
// within each batch. The curve's evidence has already passed derivation.
func (c Curve) Statistics() []BatchStatistics {
	pointCount := 0
	for _, pass := range c.passes {
		pointCount += len(pass.points)
	}
	idleTrialCount := 0
	for _, baseline := range c.baselines {
		idleTrialCount += len(baseline.Trials)
	}
	result := make([]BatchStatistics, 0, pointCount)
	for _, pass := range c.passes {
		for _, point := range pass.points {
			sampleCount := 0
			for _, trial := range point.Trials {
				sampleCount += len(trial.ClientLatencyMilliseconds)
			}
			latencies := make([]float64, 0, sampleCount)
			for _, trial := range point.Trials {
				latencies = append(latencies, trial.ClientLatencyMilliseconds...)
			}
			result = append(result, BatchStatistics{
				Batch:                        point.Batch,
				IncrementalWorkJoulesPerItem: point.MeanIncrementalWorkJoulesPerItem,
				ActiveTrialCount:             len(point.Trials),
				IdleTrialCount:               idleTrialCount,
				Confidence:                   clonePointConfidence(point.Confidence),
				LatencySampleCount:           sampleCount,
				LatencyP95Milliseconds:       percentileInPlace(latencies, 0.95),
			})
		}
	}
	return result
}

func percentile(values []float64, quantile float64) float64 {
	return percentileInPlace(append([]float64(nil), values...), quantile)
}

func percentileInPlace(values []float64, quantile float64) float64 {
	sort.Float64s(values)
	return stat.Quantile(quantile, stat.Empirical, values, nil)
}

func studentTCritical(confidenceLevel, degreesOfFreedom float64) float64 {
	return studentTQuantile(1-(1-confidenceLevel)/2, degreesOfFreedom)
}

func studentTQuantile(probability, degreesOfFreedom float64) float64 {
	return distuv.StudentsT{Mu: 0, Sigma: 1, Nu: degreesOfFreedom}.Quantile(probability)
}

type varianceComponent struct {
	variance         float64
	degreesOfFreedom float64
}

type pointStatistics struct {
	meanIncrementalWorkJoulesPerItem float64
	incrementalWorkSampleVariance    float64
	meanIdleBaselineSecondsPerItem   float64
	confidence                       PointConfidence
}

func derivePointStatistics(
	incrementalWorkJoulesPerItem []float64,
	idleBaselineSecondsPerItem []float64,
	baselineVariance float64,
	baselineDegreesOfFreedom int,
	confidenceLevel float64,
) (pointStatistics, error) {
	result := pointStatistics{}
	result.meanIncrementalWorkJoulesPerItem, result.incrementalWorkSampleVariance =
		stat.MeanVariance(incrementalWorkJoulesPerItem, nil)
	result.meanIdleBaselineSecondsPerItem = stat.Mean(idleBaselineSecondsPerItem, nil)
	activeVariance := result.incrementalWorkSampleVariance / float64(len(incrementalWorkJoulesPerItem))
	baselinePointVariance := result.meanIdleBaselineSecondsPerItem *
		result.meanIdleBaselineSecondsPerItem * baselineVariance
	variance, degreesOfFreedom, err := welchSatterthwaite(
		varianceComponent{variance: activeVariance, degreesOfFreedom: float64(len(incrementalWorkJoulesPerItem) - 1)},
		varianceComponent{variance: baselinePointVariance, degreesOfFreedom: float64(baselineDegreesOfFreedom)},
	)
	if err != nil {
		return pointStatistics{}, err
	}
	varianceCopy := variance
	result.confidence = PointConfidence{
		EstimatorVersion: PointEstimatorVersion, Method: PointConfidenceMethod,
		Level: confidenceLevel, VarianceOfMean: &varianceCopy,
		EffectiveDegreesOfFreedom: degreesOfFreedom,
	}
	if degreesOfFreedom != nil {
		result.confidence.HalfWidth = studentTQuantile(
			1-(1-confidenceLevel)/2, *degreesOfFreedom,
		) * math.Sqrt(variance)
	}
	return result, nil
}

// welchSatterthwaite combines independent variance components. A nil degree of
// freedom means every component has zero variance.
func welchSatterthwaite(components ...varianceComponent) (float64, *float64, error) {
	var totalVariance float64
	var scale float64
	for _, component := range components {
		if component.variance < 0 || math.IsNaN(component.variance) || math.IsInf(component.variance, 0) ||
			component.degreesOfFreedom <= 0 || math.IsNaN(component.degreesOfFreedom) ||
			math.IsInf(component.degreesOfFreedom, 0) {
			return 0, nil, errors.New("welch variance component is invalid")
		}
		totalVariance += component.variance
		if component.variance > scale {
			scale = component.variance
		}
	}
	if math.IsInf(totalVariance, 0) {
		return 0, nil, errors.New("welch total variance exceeds the supported range")
	}
	if scale == 0 {
		return 0, nil, nil
	}
	var scaledTotal float64
	var scaledDenominator float64
	for _, component := range components {
		scaledVariance := component.variance / scale
		scaledTotal += scaledVariance
		scaledDenominator += scaledVariance * scaledVariance / component.degreesOfFreedom
	}
	degreesOfFreedom := scaledTotal * scaledTotal / scaledDenominator
	if math.IsNaN(degreesOfFreedom) || math.IsInf(degreesOfFreedom, 0) {
		return 0, nil, errors.New("welch effective degrees of freedom exceeds the supported range")
	}
	return totalVariance, &degreesOfFreedom, nil
}
