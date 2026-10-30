package profilederivation

import (
	"errors"
	"math"
)

const (
	PointEstimatorVersion = "incremental-point-welch-satterthwaite-v2"
	PointConfidenceMethod = "student-t-welch-satterthwaite"
)

func validatePointPolicy(policy PointPolicy) error {
	if policy.Version == "" || policy.MinimumTrials < 2 || !validProbability(policy.ConfidenceLevel) {
		return errors.New("point policy must use a valid confidence level with at least two trials")
	}
	return nil
}

func validProbability(value float64) bool {
	return value > 0 && value < 1 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
