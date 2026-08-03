// Package admission projects verified execution evidence into the pure
// single-Run scientific admission boundary.
package admission

import (
	"errors"
	"fmt"
	"sort"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
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
	return compactResult(evaluated.Verdict, evaluated.PolicyVersion, evaluated.Reasons)
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
