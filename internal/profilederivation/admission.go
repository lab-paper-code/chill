package profilederivation

import (
	"errors"
	"math"
	"sort"
	"time"
)

type powerWindow struct {
	Start                    time.Time
	End                      time.Time
	Samples                  []Sample
	Failures                 int
	SourceTimestampAbsent    bool
	ConcurrentCHILLWorkloads []string
	ExternalWorkloadChanges  []string
	WorkloadWindowKnown      bool
	ThermalStateKnown        bool
	ThermallyThrottled       bool
}

type powerWindowAssessment struct {
	Reasons         []Reason
	TotalWallJoules *float64
}

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

func assessPowerWindow(window powerWindow, policy AdmissionPolicy) powerWindowAssessment {
	assessment := powerWindowAssessment{}
	add := func(kind Verdict, code, message string) {
		assessment.Reasons = append(assessment.Reasons, Reason{Kind: kind, Code: code, Message: message})
	}
	if window.Failures > 0 {
		add(VerdictInsufficient, "PowerReadFailures", "power observation contains failed reads")
	}
	if len(window.Samples) < policy.MinimumSamples {
		add(VerdictInsufficient, "InsufficientSamples", "power sample count is below policy")
	}
	if window.SourceTimestampAbsent && !policy.AllowReceiptTimestamps {
		add(VerdictInsufficient, "SourceTimestampUnavailable", "policy does not admit observer receipt timestamps")
	}
	if !window.ThermalStateKnown {
		add(VerdictInsufficient, "ThermalStateUnavailable", "thermal state was not captured")
	}
	if window.ThermallyThrottled {
		add(VerdictRejected, "ThermalOrRuntimeThrottling", "trial is marked throttled")
	}
	if policy.RejectConcurrentCHILLWorkloads && len(window.ConcurrentCHILLWorkloads) > 0 {
		add(VerdictRejected, "ConcurrentCHILLWorkload", "another CHILL measurement workload overlaps the Run")
	}
	if policy.RejectExternalWorkloadChanges && len(window.ExternalWorkloadChanges) > 0 {
		add(VerdictRejected, "ExternalWorkloadChanged", "external Pod membership changed during the measurement window")
	}
	if !window.WorkloadWindowKnown {
		add(VerdictInsufficient, "WorkloadWindowUnknown", "continuous measurement-window workload observation is unavailable")
	}
	joules, qualityErr := integratePowerWindow(window, policy)
	if qualityErr != nil {
		add(VerdictInsufficient, "PowerWindowCoverage", qualityErr.Error())
	} else {
		assessment.TotalWallJoules = &joules
	}
	return assessment
}

func integratePowerWindow(window powerWindow, policy AdmissionPolicy) (float64, error) {
	samples := append([]Sample(nil), window.Samples...)
	sort.Slice(samples, func(i, j int) bool { return samples[i].ObservedAt.Before(samples[j].ObservedAt) })
	inside := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		if math.IsNaN(sample.Watts) || math.IsInf(sample.Watts, 0) || sample.Watts < 0 {
			return 0, errors.New("invalid power sample")
		}
		if !sample.ObservedAt.Before(window.Start) && !sample.ObservedAt.After(window.End) {
			inside = append(inside, sample)
		}
	}
	if len(inside) < 2 {
		return 0, errors.New("fewer than two samples overlap the runtime window")
	}
	boundary := time.Duration(policy.MaximumBoundaryDistanceSeconds * float64(time.Second))
	gapLimit := time.Duration(policy.MaximumGapSeconds * float64(time.Second))
	if inside[0].ObservedAt.Sub(window.Start) > boundary ||
		window.End.Sub(inside[len(inside)-1].ObservedAt) > boundary {
		return 0, errors.New("sample boundary distance exceeds policy")
	}
	coverage := inside[len(inside)-1].ObservedAt.Sub(inside[0].ObservedAt).
		Seconds() /
		window.End.Sub(window.Start).
			Seconds()
	if coverage < policy.MinimumCoverage {
		return 0, errors.New("sample coverage is below policy")
	}
	points := make([]Sample, 0, len(inside)+2)
	points = append(points, Sample{ObservedAt: window.Start, Watts: inside[0].Watts})
	points = append(points, inside...)
	points = append(points, Sample{ObservedAt: window.End, Watts: inside[len(inside)-1].Watts})
	var joules float64
	for index := 1; index < len(points); index++ {
		duration := points[index].ObservedAt.Sub(points[index-1].ObservedAt)
		if duration <= 0 {
			return 0, errors.New("duplicate or non-monotonic sample timestamp")
		}
		if duration > gapLimit {
			return 0, errors.New("maximum sample gap exceeds policy")
		}
		joules += (points[index-1].Watts + points[index].Watts) / 2 * duration.Seconds()
	}
	return joules, nil
}
