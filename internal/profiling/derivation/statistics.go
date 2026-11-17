package profilederivation

import "gonum.org/v1/gonum/stat/distuv"

func studentTCritical(confidenceLevel, degreesOfFreedom float64) float64 {
	return studentTQuantile(1-(1-confidenceLevel)/2, degreesOfFreedom)
}

func studentTQuantile(probability, degreesOfFreedom float64) float64 {
	return distuv.StudentsT{Mu: 0, Sigma: 1, Nu: degreesOfFreedom}.Quantile(probability)
}
