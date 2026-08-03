package profilederivation

import (
	"math"
	"regexp"
	"strings"
)

var rawEvidenceDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// AdmitIdle applies idle-specific completeness gates and the same power-window
// quality core used by active admission.
func AdmitIdle(observation IdleObservation, policy AdmissionPolicy) IdleAdmissionResult {
	result := IdleAdmissionResult{
		PolicyVersion: policy.Version, StateIdentity: observation.StateIdentity,
		EnvironmentIdentity:         observation.EnvironmentIdentity,
		MeasurementProtocolIdentity: observation.MeasurementProtocolIdentity,
		TrialID:                     observation.TrialID, RawEvidence: observation.RawEvidence,
		WindowSeconds: observation.WindowEnd.Sub(observation.WindowStart).Seconds(),
	}
	add := func(kind Verdict, code, message string) {
		result.Reasons = append(result.Reasons, Reason{Kind: kind, Code: code, Message: message})
	}
	if !validAdmissionPolicy(policy) {
		add(VerdictRejected, "InvalidPolicy", "idle admission policy is invalid")
		result.Verdict = VerdictRejected
		return result
	}
	if observation.StateIdentity == "" || observation.EnvironmentIdentity == "" ||
		observation.MeasurementProtocolIdentity == "" || observation.TrialID == "" ||
		!observation.WindowEnd.After(observation.WindowStart) ||
		!validRawEvidenceIdentity(observation.RawEvidence) {
		add(VerdictRejected, "InvalidIdleObservation", "idle identity, provenance, or window is invalid")
	}
	if !observation.ModelLoadedBeforeSettling || !observation.SettlingWindowVerified ||
		!observation.ModelResidentAtEnd {
		add(VerdictRejected, "NonSteadyIdleWindow", "model load, settling, or residency evidence is incomplete")
	}
	if observation.InferenceCountAfterModelLoad != 0 {
		add(VerdictRejected, "InferenceDuringIdle", "idle evidence contains an inference call")
	}
	window := assessPowerWindow(powerWindowFromIdle(observation), policy)
	result.Reasons = append(result.Reasons, window.Reasons...)
	result.TotalWallJoules = window.TotalWallJoules
	if window.TotalWallJoules != nil && result.WindowSeconds > 0 {
		mean := *window.TotalWallJoules / result.WindowSeconds
		if !math.IsNaN(mean) && !math.IsInf(mean, 0) && mean >= 0 {
			result.MeanWallWatts = &mean
		}
	}
	result.Verdict = verdictFromReasons(result.Reasons)
	return result
}

func powerWindowFromIdle(observation IdleObservation) powerWindow {
	return powerWindow{
		Start: observation.WindowStart, End: observation.WindowEnd, Samples: observation.Samples,
		Failures: observation.Failures, SourceTimestampAbsent: observation.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads: observation.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:  observation.ExternalWorkloadChanges,
		WorkloadWindowKnown:      observation.WorkloadWindowKnown,
		ThermalStateKnown:        observation.ThermalStateKnown,
		ThermallyThrottled:       observation.ThermallyThrottled,
	}
}

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

func validRawEvidenceIdentity(identity RawEvidenceIdentity) bool {
	return strings.TrimSpace(identity.Store) == identity.Store && identity.Store != "" &&
		strings.TrimSpace(identity.Key) == identity.Key && identity.Key != "" &&
		rawEvidenceDigestPattern.MatchString(identity.Digest) && identity.Size > 0
}

func rawEvidenceIdentityKey(identity RawEvidenceIdentity) string {
	return identity.Store + "\x00" + identity.Key + "\x00" + identity.Digest
}
