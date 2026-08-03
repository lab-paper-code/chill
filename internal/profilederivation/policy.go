package profilederivation

import "errors"

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
	if policy.Version == "" || policy.MinimumTrials < 3 || policy.ConfidenceMethod != "student-t" ||
		policy.ConfidenceLevel != 0.95 {
		return errors.New("point policy must use 95% Student-t with at least three trials")
	}
	return nil
}
