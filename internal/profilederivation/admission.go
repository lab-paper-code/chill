package profilederivation

import (
	"errors"
	"math"
	"sort"
	"time"
)

//nolint:gocyclo // Admission intentionally keeps every fail-closed scientific gate visible in one ordered audit.
func Admit(trial Trial, policy AdmissionPolicy) AdmissionResult {
	result := AdmissionResult{
		PolicyVersion:  policy.Version,
		StateIdentity:  trial.StateIdentity,
		TrialID:        trial.TrialID,
		WindowSeconds:  trial.WindowEnd.Sub(trial.WindowStart).Seconds(),
		CompletedItems: trial.CompletedItems,
	}
	add := func(kind Verdict, code, message string) {
		result.Reasons = append(result.Reasons, Reason{Kind: kind, Code: code, Message: message})
	}
	if policy.Version == "" || policy.MinimumSamples < 2 || policy.MaximumGapSeconds <= 0 ||
		policy.MaximumBoundaryDistanceSeconds < 0 ||
		policy.MinimumCoverage <= 0 ||
		policy.MinimumCoverage > 1 {
		add(VerdictRejected, "InvalidPolicy", "admission policy is invalid")
		result.Verdict = VerdictRejected
		return result
	}
	if trial.StateIdentity == "" || trial.TrialID == "" || trial.Batch < 1 ||
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
	if trial.Failures > 0 {
		add(VerdictInsufficient, "PowerReadFailures", "power observation contains failed reads")
	}
	if len(trial.Samples) < policy.MinimumSamples {
		add(VerdictInsufficient, "InsufficientSamples", "power sample count is below policy")
	}
	if trial.SourceTimestampAbsent && !policy.AllowReceiptTimestamps {
		add(VerdictInsufficient, "SourceTimestampUnavailable", "policy does not admit observer receipt timestamps")
	}
	if !trial.ThermalStateKnown {
		add(VerdictInsufficient, "ThermalStateUnavailable", "thermal state was not captured")
	}
	if trial.ThermallyThrottled {
		add(VerdictRejected, "ThermalOrRuntimeThrottling", "trial is marked throttled")
	}
	if policy.RejectUnexpectedCoResidents && len(trial.UnexpectedCoResidents) > 0 {
		add(VerdictRejected, "UnexpectedCoResidents", "whole-node wall power is contaminated by unexpected workloads")
	}
	if !trial.CoResidentWindowKnown {
		add(VerdictInsufficient, "CoResidentWindowUnknown", "post-run snapshot cannot prove window cleanliness")
	}
	joules, qualityErr := integrate(trial, policy)
	if qualityErr != nil {
		add(VerdictInsufficient, "PowerWindowCoverage", qualityErr.Error())
	} else {
		result.TotalWallJoules = &joules
	}
	result.Verdict = VerdictAccepted
	for _, reason := range result.Reasons {
		if reason.Kind == VerdictRejected {
			result.Verdict = VerdictRejected
			break
		}
		if reason.Kind == VerdictInsufficient {
			result.Verdict = VerdictInsufficient
		}
	}
	return result
}

func integrate(trial Trial, policy AdmissionPolicy) (float64, error) {
	samples := append([]Sample(nil), trial.Samples...)
	sort.Slice(samples, func(i, j int) bool { return samples[i].ObservedAt.Before(samples[j].ObservedAt) })
	inside := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		if math.IsNaN(sample.Watts) || math.IsInf(sample.Watts, 0) || sample.Watts < 0 {
			return 0, errors.New("invalid power sample")
		}
		if !sample.ObservedAt.Before(trial.WindowStart) && !sample.ObservedAt.After(trial.WindowEnd) {
			inside = append(inside, sample)
		}
	}
	if len(inside) < 2 {
		return 0, errors.New("fewer than two samples overlap the runtime window")
	}
	boundary := time.Duration(policy.MaximumBoundaryDistanceSeconds * float64(time.Second))
	gapLimit := time.Duration(policy.MaximumGapSeconds * float64(time.Second))
	if inside[0].ObservedAt.Sub(trial.WindowStart) > boundary ||
		trial.WindowEnd.Sub(inside[len(inside)-1].ObservedAt) > boundary {
		return 0, errors.New("sample boundary distance exceeds policy")
	}
	coverage := inside[len(inside)-1].ObservedAt.Sub(inside[0].ObservedAt).
		Seconds() /
		trial.WindowEnd.Sub(trial.WindowStart).
			Seconds()
	if coverage < policy.MinimumCoverage {
		return 0, errors.New("sample coverage is below policy")
	}
	points := make([]Sample, 0, len(inside)+2)
	points = append(points, Sample{ObservedAt: trial.WindowStart, Watts: inside[0].Watts})
	points = append(points, inside...)
	points = append(points, Sample{ObservedAt: trial.WindowEnd, Watts: inside[len(inside)-1].Watts})
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
