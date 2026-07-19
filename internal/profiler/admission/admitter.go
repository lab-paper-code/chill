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

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const WallPowerPolicyV1 = "wall-power-v1"

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
	if name != WallPowerPolicyV1 {
		return profilederivation.AdmissionPolicy{}, fmt.Errorf("unsupported admission policy %q", name)
	}
	return profilederivation.AdmissionPolicy{
		Version:                        WallPowerPolicyV1,
		MinimumSamples:                 20,
		MaximumGapSeconds:              1.5,
		MaximumBoundaryDistanceSeconds: 1.0,
		MinimumCoverage:                0.95,
		AllowReceiptTimestamps:         true,
		RejectUnexpectedCoResidents:    true,
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
	samples := make([]profilederivation.Sample, len(raw.Power.Samples))
	for index, sample := range raw.Power.Samples {
		samples[index] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	unexpected := make([]string, 0)
	for _, pod := range execution.Terminal.NodeWorkload.Pods {
		if pod.Phase == "Running" && pod.UID != execution.Terminal.Pod.UID {
			unexpected = append(unexpected, pod.Namespace+"/"+pod.Name)
		}
	}
	sort.Strings(unexpected)
	stateIdentity, err := materialStateIdentity(execution.Intent, execution.Terminal.Node.UID)
	if err != nil {
		return profilederivation.Trial{}, err
	}
	return profilederivation.Trial{
		StateIdentity:         stateIdentity,
		TrialID:               execution.Terminal.Pod.UID,
		Batch:                 execution.Intent.Measurement.Batch,
		WindowStart:           windowStart,
		WindowEnd:             windowEnd,
		CompletedCalls:        raw.Runtime.InferenceCount,
		CompletedItems:        raw.Runtime.InferenceCount * execution.Intent.Measurement.Batch,
		LatencyMilliseconds:   latencies,
		Samples:               samples,
		Failures:              len(raw.Power.Failures),
		SourceTimestampAbsent: raw.Power.Summary.SourceTimestampAbsent,
		UnexpectedCoResidents: unexpected,
		CoResidentWindowKnown: false,
		ThermalStateKnown:     false,
		SteadyWindowVerified:  true,
	}, nil
}

func materialStateIdentity(intent profilingrun.Intent, nodeUID string) (string, error) {
	measurement := intent.Measurement
	measurement.Batch = 0
	payload, err := json.Marshal(struct {
		State       profilingrun.ExecutionState      `json:"state"`
		Images      profilingrun.Images              `json:"images"`
		NodeUID     string                           `json:"nodeUID"`
		CPU         profilingrun.CPUContract         `json:"cpu"`
		Measurement profilingrun.MeasurementContract `json:"measurement"`
		Power       profilingrun.PowerContract       `json:"power"`
	}{intent.State, intent.Images, nodeUID, intent.CPU, measurement, intent.Power})
	if err != nil {
		return "", fmt.Errorf("encode material execution state: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
