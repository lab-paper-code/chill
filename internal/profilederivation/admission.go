package profilederivation

import (
	"math"
)

//nolint:gocyclo // Admission intentionally keeps every fail-closed scientific gate visible in one ordered audit.
func Admit(trial Trial, policy AdmissionPolicy) AdmissionResult {
	result := AdmissionResult{
		PolicyVersion:               policy.Version,
		StateIdentity:               trial.StateIdentity,
		EnvironmentIdentity:         trial.EnvironmentIdentity,
		MeasurementProtocolIdentity: trial.MeasurementProtocolIdentity,
		TrialID:                     trial.TrialID,
		WindowSeconds:               trial.WindowEnd.Sub(trial.WindowStart).Seconds(),
		CompletedItems:              trial.CompletedItems,
	}
	add := func(kind Verdict, code, message string) {
		result.Reasons = append(result.Reasons, Reason{Kind: kind, Code: code, Message: message})
	}
	if !validAdmissionPolicy(policy) {
		add(VerdictRejected, "InvalidPolicy", "admission policy is invalid")
		result.Verdict = VerdictRejected
		return result
	}
	if trial.StateIdentity == "" || trial.EnvironmentIdentity == "" ||
		trial.MeasurementProtocolIdentity == "" || trial.TrialID == "" || trial.Batch < 1 ||
		!trial.WindowEnd.After(trial.WindowStart) ||
		trial.CompletedCalls < 1 ||
		trial.CompletedItems != trial.CompletedCalls*trial.Batch ||
		len(trial.LatencyMilliseconds) != trial.CompletedCalls {
		add(VerdictRejected, "InvalidTrial", "trial identity, window, denominator, or latency evidence is invalid")
	}
	for _, latency := range trial.LatencyMilliseconds {
		if latency <= 0 || math.IsNaN(latency) || math.IsInf(latency, 0) {
			add(VerdictRejected, "InvalidLatency", "latency samples must be positive and finite")
			break
		}
	}
	if !trial.SteadyWindowVerified {
		add(VerdictRejected, "NonSteadyWindow", "warm-up, load, or preparation overlaps the measurement window")
	}
	window := assessPowerWindow(powerWindowFromTrial(trial), policy)
	result.Reasons = append(result.Reasons, window.Reasons...)
	result.TotalWallJoules = window.TotalWallJoules
	result.Verdict = verdictFromReasons(result.Reasons)
	return result
}

func powerWindowFromTrial(trial Trial) powerWindow {
	return powerWindow{
		Start: trial.WindowStart, End: trial.WindowEnd, Samples: trial.Samples, Failures: trial.Failures,
		SourceTimestampAbsent:    trial.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads: trial.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:  trial.ExternalWorkloadChanges,
		WorkloadWindowKnown:      trial.WorkloadWindowKnown,
		ThermalStateKnown:        trial.ThermalStateKnown,
		ThermallyThrottled:       trial.ThermallyThrottled,
	}
}
