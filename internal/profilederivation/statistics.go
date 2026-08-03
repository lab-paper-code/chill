package profilederivation

import (
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

func mean(values []float64) float64 {
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func studentTHalfWidth(values []float64) float64 {
	if len(values) < 3 {
		return math.Inf(1)
	}
	m := mean(values)
	var sum float64
	for _, value := range values {
		delta := value - m
		sum += delta * delta
	}
	critical := studentT95(len(values) - 1)
	return critical * math.Sqrt(sum/float64(len(values)-1)) / math.Sqrt(float64(len(values)))
}

func studentT95(df int) float64 {
	return distuv.StudentsT{Mu: 0, Sigma: 1, Nu: float64(df)}.Quantile(0.975)
}
