package profilederivation

import (
	"math"
)

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
