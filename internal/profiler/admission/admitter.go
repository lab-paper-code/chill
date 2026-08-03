// Package admission projects verified execution evidence into the pure
// single-Run scientific admission boundary.
package admission

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
)

const (
	WallPowerPolicyV2     = "wall-power-v2"
	IdleWallPowerPolicyV1 = "idle-wall-power-v1"
)

type Verdict string

const (
	VerdictAccepted     Verdict = "Accepted"
	VerdictRejected     Verdict = "Rejected"
	VerdictInsufficient Verdict = "Insufficient"
)

// Result is the compact admission fact suitable for control-plane status.
// Raw diagnostic messages and derived energy remain in the scientific layer.
type Result struct {
	Verdict       Verdict
	PolicyVersion string
	ReasonCodes   []string
}

func Evaluate(
	execution profilerevidence.ExecutionEvidence,
	raw profilerevidence.RawMeasurement,
	policyName string,
) (Result, error) {
	policy, err := builtInPolicy(policyName)
	if err != nil {
		return Result{}, err
	}
	if err := execution.Validate(raw); err != nil {
		return Result{}, fmt.Errorf("validate execution evidence: %w", err)
	}
	trial, err := projectTrial(execution, raw)
	if err != nil {
		return Result{}, err
	}
	evaluated := profilederivation.Admit(trial, policy)
	reasonCodes := make([]string, 0, len(evaluated.Reasons))
	seen := make(map[string]struct{}, len(evaluated.Reasons))
	for _, reason := range evaluated.Reasons {
		if reason.Code == "" {
			return Result{}, errors.New("admission result contains an empty reason code")
		}
		if _, exists := seen[reason.Code]; !exists {
			seen[reason.Code] = struct{}{}
			reasonCodes = append(reasonCodes, reason.Code)
		}
	}
	sort.Strings(reasonCodes)
	return Result{
		Verdict: Verdict(evaluated.Verdict), PolicyVersion: evaluated.PolicyVersion, ReasonCodes: reasonCodes,
	}, nil
}

// EvaluateIdle validates the full immutable evidence chain, applies the named
// idle policy, and projects an IdleTrial only for Accepted evidence.
func EvaluateIdle(
	execution profilerevidence.ExecutionEvidence,
	raw profilerevidence.IdleRawMeasurement,
	policyName string,
) (Result, *profilederivation.IdleTrial, error) {
	policy, err := builtInIdlePolicy(policyName)
	if err != nil {
		return Result{}, nil, err
	}
	if err := execution.ValidateIdle(raw); err != nil {
		return Result{}, nil, fmt.Errorf("validate idle execution evidence: %w", err)
	}
	observation, err := projectIdleObservation(execution, raw)
	if err != nil {
		return Result{}, nil, err
	}
	evaluated := profilederivation.AdmitIdle(observation, policy)
	compact, err := compactResult(evaluated.Verdict, evaluated.PolicyVersion, evaluated.Reasons)
	if err != nil {
		return Result{}, nil, err
	}
	if evaluated.Verdict != profilederivation.VerdictAccepted {
		return compact, nil, nil
	}
	if evaluated.MeanWallWatts == nil {
		return Result{}, nil, errors.New("accepted idle admission omitted mean wall power")
	}
	trial := &profilederivation.IdleTrial{
		StateIdentity: evaluated.StateIdentity, EnvironmentIdentity: evaluated.EnvironmentIdentity,
		MeasurementProtocolIdentity: evaluated.MeasurementProtocolIdentity, TrialID: evaluated.TrialID,
		RawEvidence: evaluated.RawEvidence, AdmissionPolicyVersion: evaluated.PolicyVersion,
		MeanWallWatts: *evaluated.MeanWallWatts,
	}
	return compact, trial, nil
}

func builtInPolicy(name string) (profilederivation.AdmissionPolicy, error) {
	if name != WallPowerPolicyV2 {
		return profilederivation.AdmissionPolicy{}, fmt.Errorf("unsupported admission policy %q", name)
	}
	return profilederivation.AdmissionPolicy{
		Version:                        WallPowerPolicyV2,
		MinimumSamples:                 20,
		MaximumGapSeconds:              1.5,
		MaximumBoundaryDistanceSeconds: 1.0,
		MinimumCoverage:                0.95,
		AllowReceiptTimestamps:         true,
		RejectConcurrentCHILLWorkloads: true,
		RejectExternalWorkloadChanges:  true,
	}, nil
}

func builtInIdlePolicy(name string) (profilederivation.AdmissionPolicy, error) {
	if name != IdleWallPowerPolicyV1 {
		return profilederivation.AdmissionPolicy{}, fmt.Errorf("unsupported idle admission policy %q", name)
	}
	policy, err := builtInPolicy(WallPowerPolicyV2)
	if err != nil {
		return profilederivation.AdmissionPolicy{}, err
	}
	policy.Version = IdleWallPowerPolicyV1
	return policy, nil
}

func compactResult(verdict profilederivation.Verdict, policyVersion string, reasons []profilederivation.Reason) (Result, error) {
	reasonCodes := make([]string, 0, len(reasons))
	seen := make(map[string]struct{}, len(reasons))
	for _, reason := range reasons {
		if reason.Code == "" {
			return Result{}, errors.New("admission result contains an empty reason code")
		}
		if _, exists := seen[reason.Code]; !exists {
			seen[reason.Code] = struct{}{}
			reasonCodes = append(reasonCodes, reason.Code)
		}
	}
	sort.Strings(reasonCodes)
	return Result{Verdict: Verdict(verdict), PolicyVersion: policyVersion, ReasonCodes: reasonCodes}, nil
}

func projectIdleObservation(
	execution profilerevidence.ExecutionEvidence,
	raw profilerevidence.IdleRawMeasurement,
) (profilederivation.IdleObservation, error) {
	windowStart, windowEnd, err := raw.Runtime.MeasurementWindow()
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	modelLoaded, err := time.Parse(time.RFC3339Nano, raw.Runtime.ModelLoadCompletedAt)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	settlingStarted, err := time.Parse(time.RFC3339Nano, raw.Runtime.SettlingStartedAt)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	settlingCompleted, err := time.Parse(time.RFC3339Nano, raw.Runtime.SettlingCompletedAt)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	samples := make([]profilederivation.Sample, len(raw.Metrics.Samples))
	for index, sample := range raw.Metrics.Samples {
		samples[index] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	thermalKnown, thermallyThrottled := thermalState(raw.Metrics.Samples, windowStart, windowEnd)
	workload, err := projectWorkloadWindow(execution.Intent, raw.Workload, windowStart, windowEnd)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	identities, err := ProjectRunIdentities(execution.Intent, execution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.IdleObservation{}, err
	}
	return profilederivation.IdleObservation{
		StateIdentity: identities.StateIdentity, EnvironmentIdentity: workload.EnvironmentIdentity,
		MeasurementProtocolIdentity: identities.MeasurementProtocolIdentity,
		TrialID:                     execution.Terminal.Pod.UID,
		RawEvidence: profilederivation.RawEvidenceIdentity{
			Store: execution.RawMeasurement.Store, Key: execution.RawMeasurement.Key,
			Digest: execution.RawMeasurement.Digest, Size: execution.RawMeasurement.Size,
		},
		WindowStart: windowStart, WindowEnd: windowEnd,
		ModelLoadedBeforeSettling: !modelLoaded.After(settlingStarted),
		SettlingWindowVerified: !settlingCompleted.After(windowStart) &&
			settlingCompleted.Sub(settlingStarted) >= time.Duration(execution.Intent.Measurement.SettlingDurationSeconds)*time.Second,
		InferenceCountAfterModelLoad: raw.Runtime.InferenceCountAfterModelLoad,
		ModelResidentAtEnd:           raw.Runtime.ModelResidentAtEnd,
		Samples:                      samples, Failures: len(raw.Metrics.Failures),
		SourceTimestampAbsent:    raw.Metrics.Summary.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads: workload.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:  workload.ExternalWorkloadChanges,
		WorkloadWindowKnown:      workload.Known, ThermalStateKnown: thermalKnown,
		ThermallyThrottled: thermallyThrottled,
	}, nil
}

func projectTrial(
	execution profilerevidence.ExecutionEvidence,
	raw profilerevidence.RawMeasurement,
) (profilederivation.Trial, error) {
	windowStart, windowEnd, err := raw.Runtime.MeasurementWindow()
	if err != nil {
		return profilederivation.Trial{}, err
	}
	latencies := make([]float64, 0, raw.Runtime.InferenceCount)
	for _, repetition := range raw.Runtime.RepetitionResults {
		latencies = append(latencies, repetition.LatencyMilliseconds...)
	}
	samples := make([]profilederivation.Sample, len(raw.Metrics.Samples))
	for index, sample := range raw.Metrics.Samples {
		samples[index] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	thermalKnown, thermallyThrottled := thermalState(raw.Metrics.Samples, windowStart, windowEnd)
	workload, err := projectWorkloadWindow(execution.Intent, raw.Workload, windowStart, windowEnd)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	identities, err := ProjectRunIdentities(execution.Intent, execution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	return profilederivation.Trial{
		StateIdentity:               identities.StateIdentity,
		EnvironmentIdentity:         workload.EnvironmentIdentity,
		MeasurementProtocolIdentity: identities.MeasurementProtocolIdentity,
		TrialID:                     execution.Terminal.Pod.UID,
		Batch:                       execution.Intent.Measurement.Batch,
		WindowStart:                 windowStart,
		WindowEnd:                   windowEnd,
		CompletedCalls:              raw.Runtime.InferenceCount,
		CompletedItems:              raw.Runtime.InferenceCount * execution.Intent.Measurement.Batch,
		LatencyMilliseconds:         latencies,
		Samples:                     samples,
		Failures:                    len(raw.Metrics.Failures),
		SourceTimestampAbsent:       raw.Metrics.Summary.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads:    workload.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:     workload.ExternalWorkloadChanges,
		WorkloadWindowKnown:         workload.Known,
		ThermalStateKnown:           thermalKnown,
		ThermallyThrottled:          thermallyThrottled,
		SteadyWindowVerified:        true,
	}, nil
}

func thermalState(samples []metricobserver.Sample, windowStart, windowEnd time.Time) (known, throttled bool) {
	inside := make([]metricobserver.Sample, 0, len(samples))
	for _, sample := range samples {
		if !sample.ObservedAt.Before(windowStart) && !sample.ObservedAt.After(windowEnd) {
			inside = append(inside, sample)
		}
	}
	if len(inside) == 0 {
		return false, false
	}

	available := true
	directComplete := true
	coreComplete := true
	packageComplete := true
	counterReset := false
	var previousCore, previousPackage *uint64
	for _, sample := range inside {
		thermal := sample.Thermal
		if thermal == nil || thermal.ObservationAvailable == nil || !*thermal.ObservationAvailable {
			available = false
		}
		if thermal == nil {
			directComplete, coreComplete, packageComplete = false, false, false
			continue
		}
		if thermal.Active == nil {
			directComplete = false
		} else if *thermal.Active {
			return true, true
		}
		if thermal.CPUCoreThrottleEvents == nil {
			coreComplete = false
		} else {
			if previousCore != nil {
				if *thermal.CPUCoreThrottleEvents > *previousCore {
					return true, true
				}
				if *thermal.CPUCoreThrottleEvents < *previousCore {
					counterReset = true
				}
			}
			value := *thermal.CPUCoreThrottleEvents
			previousCore = &value
		}
		if thermal.CPUPackageThrottleEvents == nil {
			packageComplete = false
		} else {
			if previousPackage != nil {
				if *thermal.CPUPackageThrottleEvents > *previousPackage {
					return true, true
				}
				if *thermal.CPUPackageThrottleEvents < *previousPackage {
					counterReset = true
				}
			}
			value := *thermal.CPUPackageThrottleEvents
			previousPackage = &value
		}
	}
	return available && !counterReset && (directComplete || coreComplete || packageComplete), false
}
