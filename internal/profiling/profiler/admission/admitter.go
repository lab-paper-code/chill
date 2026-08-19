// Package admission projects verified execution evidence into the pure
// single-Run scientific admission boundary.
package admission

import (
	"errors"
	"sort"

	"github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
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
	evidence profilerevidence.VerifiedEvidence,
	frozenPolicy FrozenPolicy,
) (Result, error) {
	result, _, err := EvaluateTrial(evidence, frozenPolicy)
	return result, err
}

// EvaluateTrial validates the immutable evidence chain, applies the named
// policy, and projects the derivation trial only for Accepted evidence.
func EvaluateTrial(
	evidence profilerevidence.VerifiedEvidence,
	frozenPolicy FrozenPolicy,
) (Result, *profilederivation.Trial, error) {
	result, run, err := EvaluateTrialAdmission(evidence, frozenPolicy)
	if err != nil || run == nil {
		return result, nil, err
	}
	trial := run.Trial
	return result, &trial, nil
}

// EvaluateTrialAdmission validates the immutable evidence chain, applies the named
// policy, and retains the admission result beside its Accepted trial.
func EvaluateTrialAdmission(
	evidence profilerevidence.VerifiedEvidence,
	frozenPolicy FrozenPolicy,
) (Result, *profilederivation.TrialAdmission, error) {
	execution, raw, err := evidence.Records()
	if err != nil {
		return Result{}, nil, err
	}
	policy, err := frozenPolicy.AdmissionPolicy()
	if err != nil {
		return Result{}, nil, err
	}
	_, frozenRecord, err := frozenPolicy.Records()
	if err != nil {
		return Result{}, nil, err
	}
	if frozenRecord.WorkloadMode != execution.Intent.Measurement.WorkloadMode {
		return Result{}, nil, errors.New("admission policy workload mode does not match execution intent")
	}
	trial, err := projectTrial(execution, raw)
	if err != nil {
		return Result{}, nil, err
	}
	evaluated := profilederivation.Admit(trial, policy)
	compact, err := compactResult(evaluated.Verdict, evaluated.PolicyVersion, evaluated.Reasons)
	if err != nil {
		return Result{}, nil, err
	}
	if evaluated.Verdict != profilederivation.VerdictAccepted {
		return compact, nil, nil
	}
	return compact, &profilederivation.TrialAdmission{Trial: trial, Admission: evaluated}, nil
}

// EvaluateIdle validates the full immutable evidence chain, applies the named
// idle policy, and projects an IdleTrial only for Accepted evidence.
func EvaluateIdle(
	evidence profilerevidence.VerifiedIdleEvidence,
	frozenPolicy FrozenPolicy,
) (Result, *profilederivation.IdleTrial, error) {
	execution, raw, err := evidence.Records()
	if err != nil {
		return Result{}, nil, err
	}
	policy, err := frozenPolicy.AdmissionPolicy()
	if err != nil {
		return Result{}, nil, err
	}
	_, frozenRecord, err := frozenPolicy.Records()
	if err != nil {
		return Result{}, nil, err
	}
	if frozenRecord.WorkloadMode != execution.Intent.Measurement.WorkloadMode {
		return Result{}, nil, errors.New("idle admission policy workload mode does not match execution intent")
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
	if evaluated.ModelLoadedIdleMeanWallWatts == nil {
		return Result{}, nil, errors.New("accepted idle admission omitted mean wall power")
	}
	trial := &profilederivation.IdleTrial{
		StateIdentity: evaluated.StateIdentity, EnvironmentIdentity: evaluated.EnvironmentIdentity,
		MeasurementProtocolIdentity: evaluated.MeasurementProtocolIdentity, TrialID: evaluated.TrialID,
		RawEvidence: evaluated.RawEvidence, AdmissionPolicyVersion: evaluated.PolicyVersion,
		ModelLoadedIdleMeanWallWatts: *evaluated.ModelLoadedIdleMeanWallWatts,
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
