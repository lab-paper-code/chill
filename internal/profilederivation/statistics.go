package profilederivation

import (
	"errors"
	"math"
	"sort"

	"gonum.org/v1/gonum/stat"
	"gonum.org/v1/gonum/stat/distuv"
)

func percentile(values []float64, quantile float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	return stat.Quantile(quantile, stat.Empirical, ordered, nil)
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
	meanIncrementalJoulesPerItem          float64
	incrementalSampleVariance             float64
	meanBaselineCoefficientSecondsPerItem float64
	confidence                            PointConfidence
}

func derivePointStatistics(
	values []float64,
	coefficients []float64,
	baselineVariance float64,
	baselineDegreesOfFreedom int,
	confidenceLevel float64,
) (pointStatistics, error) {
	result := pointStatistics{}
	result.meanIncrementalJoulesPerItem, result.incrementalSampleVariance = stat.MeanVariance(values, nil)
	result.meanBaselineCoefficientSecondsPerItem = stat.Mean(coefficients, nil)
	activeVariance := result.incrementalSampleVariance / float64(len(values))
	baselinePointVariance := result.meanBaselineCoefficientSecondsPerItem *
		result.meanBaselineCoefficientSecondsPerItem * baselineVariance
	variance, degreesOfFreedom, err := welchSatterthwaite(
		varianceComponent{variance: activeVariance, degreesOfFreedom: float64(len(values) - 1)},
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

func equalWithinTolerance(left, right float64) bool {
	return math.Abs(left-right) <= 1e-12*math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
}
