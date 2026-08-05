package profilederivation

import (
	"errors"
	"math"
)

const (
	PointEstimatorVersion            = "incremental-point-welch-satterthwaite-v2"
	PointConfidenceMethod            = "student-t-welch-satterthwaite"
	PreviousPointEstimatorVersion    = "incremental-point-rss-half-width-v1"
	PreviousPointConfidenceMethod    = "student-t"
	PointConfidenceLevel             = 0.95
	BSatConfirmatorySizingMethod     = "bonferroni-welch-relative-precision-v1"
	BSatConfirmatoryEstimatorVersion = "bonferroni-welch-domain-v1"
	bsatFamilyWiseAlpha              = 0.05
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
	if policy.Version == "" || policy.MinimumTrials < 3 {
		return errors.New("point policy must use 95% Student-t with at least three trials")
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
		policy.KMax > math.MaxInt/policy.DiscoveryTrials ||
		policy.MaximumActiveRunBudget < policy.KMax*policy.DiscoveryTrials {
		return errors.New("batch-saturation policy is invalid")
	}
	return nil
}
