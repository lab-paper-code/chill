// Package admission projects verified execution evidence into the pure
// single-Run scientific admission boundary.
package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const WallPowerPolicyV2 = "wall-power-v2"

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
	workload, err := projectWorkloadWindow(execution.Intent, raw.Workload)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	stateIdentity, err := materialStateIdentity(
		execution.Intent, execution.Terminal.Node.UID, workload.ExternalEnvironmentIdentity,
	)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	return profilederivation.Trial{
		StateIdentity:            stateIdentity,
		TrialID:                  execution.Terminal.Pod.UID,
		Batch:                    execution.Intent.Measurement.Batch,
		WindowStart:              windowStart,
		WindowEnd:                windowEnd,
		CompletedCalls:           raw.Runtime.InferenceCount,
		CompletedItems:           raw.Runtime.InferenceCount * execution.Intent.Measurement.Batch,
		LatencyMilliseconds:      latencies,
		Samples:                  samples,
		Failures:                 len(raw.Metrics.Failures),
		SourceTimestampAbsent:    raw.Metrics.Summary.SourceTimestampAbsent,
		ConcurrentCHILLWorkloads: workload.ConcurrentCHILLWorkloads,
		ExternalWorkloadChanges:  workload.ExternalWorkloadChanges,
		WorkloadWindowKnown:      workload.Known,
		ThermalStateKnown:        thermalKnown,
		ThermallyThrottled:       thermallyThrottled,
		SteadyWindowVerified:     true,
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

func materialStateIdentity(intent profilingrun.Intent, nodeUID, externalEnvironmentIdentity string) (string, error) {
	measurement := intent.Measurement
	measurement.Batch = 0
	type materialState struct {
		State                       profilingrun.ExecutionState      `json:"state"`
		Images                      profilingrun.Images              `json:"images"`
		NodeUID                     string                           `json:"nodeUID"`
		CPU                         profilingrun.CPUContract         `json:"cpu"`
		Measurement                 profilingrun.MeasurementContract `json:"measurement"`
		Metrics                     profilingrun.MetricContract      `json:"metrics"`
		ExternalEnvironmentIdentity string                           `json:"externalEnvironmentIdentity,omitempty"`
	}
	base := materialState{
		State: intent.State, Images: intent.Images, NodeUID: nodeUID, CPU: intent.CPU,
		Measurement: measurement, Metrics: intent.Metrics,
		ExternalEnvironmentIdentity: externalEnvironmentIdentity,
	}
	var payload []byte
	var err error
	if intent.Workload.SchemaVersion == "" && intent.Workload.AllowedBackground == nil {
		payload, err = json.Marshal(base)
	} else {
		payload, err = json.Marshal(struct {
			materialState
			Workload profilingrun.WorkloadContract `json:"workload"`
		}{base, intent.Workload})
	}
	if err != nil {
		return "", fmt.Errorf("encode material execution state: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
