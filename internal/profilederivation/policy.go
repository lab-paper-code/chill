package profilederivation

import (
	"errors"
	"math"
)

const (
	PointEstimatorVersion            = "incremental-point-welch-satterthwaite-v2"
	PointConfidenceMethod            = "student-t-welch-satterthwaite"
	BSatConfirmatorySizingMethod     = "bonferroni-welch-relative-precision-v1"
	BSatConfirmatoryEstimatorVersion = "bonferroni-welch-domain-v1"
)

func validAdmissionPolicy(policy AdmissionPolicy) bool {
	return policy.Version != "" && policy.MinimumSamples >= 2 && policy.MaximumGapSeconds > 0 &&
		policy.MaximumBoundaryDistanceSeconds >= 0 && policy.MinimumCoverage > 0 && policy.MinimumCoverage <= 1
}

func verdictFromReasons(reasons []Reason) Verdict {
	verdict := VerdictAccepted
	for _, reason := range reasons {
		if reason.Kind == VerdictRejected {
			return VerdictRejected
		}
		if reason.Kind == VerdictInsufficient {
			verdict = VerdictInsufficient
		}
	}
	return verdict
}

func validatePointPolicy(policy PointPolicy) error {
	if policy.Version == "" || policy.MinimumTrials < 3 || !validProbability(policy.ConfidenceLevel) {
		return errors.New("point policy must use a valid confidence level with at least three trials")
	}
	return nil
}

func validateBSatPolicy(policy BSatPolicy) error {
	if policy.Version == "" || policy.KMax < 2 ||
		policy.RelativeImprovementTolerance < 0 || policy.RelativeImprovementTolerance >= 1 ||
		math.IsNaN(policy.RelativeImprovementTolerance) ||
		math.IsInf(policy.RelativeImprovementTolerance, 0) ||
		policy.MinimumPointTrials < 3 || policy.DiscoveryTrials < policy.MinimumPointTrials ||
		policy.MaximumConfirmatoryTrials < policy.MinimumPointTrials ||
		policy.FamilyWiseAlpha <= 0 || policy.FamilyWiseAlpha >= 0.5 ||
		math.IsNaN(policy.FamilyWiseAlpha) || math.IsInf(policy.FamilyWiseAlpha, 0) ||
		policy.KMax > math.MaxInt/policy.DiscoveryTrials ||
		policy.MaximumActiveRunBudget < policy.KMax*policy.DiscoveryTrials {
		return errors.New("batch-saturation policy is invalid")
	}
	return nil
}

func validProbability(value float64) bool {
	return value > 0 && value < 1 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
