package profilederivation

import (
	"math"
	"sort"
)

func percentile(values []float64, quantile float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	index := int(math.Ceil(quantile*float64(len(ordered)))) - 1
	if index < 0 {
		index = 0
	}
	return ordered[index]
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
	table := []float64{
		0,
		12.706,
		4.303,
		3.182,
		2.776,
		2.571,
		2.447,
		2.365,
		2.306,
		2.262,
		2.228,
		2.201,
		2.179,
		2.160,
		2.145,
		2.131,
		2.120,
		2.110,
		2.101,
		2.093,
		2.086,
		2.080,
		2.074,
		2.069,
		2.064,
		2.060,
		2.056,
		2.052,
		2.048,
		2.045,
		2.042,
	}
	if df < len(table) {
		return table[df]
	}
	return 1.96
}
