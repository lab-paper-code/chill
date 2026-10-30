package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"

	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
)

const energyObjectivePointPolicyVersion = "energy-objective-point-v1"

const (
	energyLocalMinimumOutcome    = "MinimumFound/" + string(profilederivation.EnergySearchLocalMinimum)
	energySearchExhaustedOutcome = "MinimumFound/" + string(profilederivation.EnergySearchSpaceExhausted)
)

// CurveEvidenceLoader retrieves evidence whose immutable objects have crossed
// the profile-evidence verification boundary. Its return types do not encode a
// binding to the supplied ProfilingRun; the composition-root adapter must
// validate that binding before returning them.
type CurveEvidenceLoader interface {
	Load(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		evidencestore.ObjectIdentity,
	) (profilerevidence.VerifiedEvidence, error)
	LoadIdle(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		evidencestore.ObjectIdentity,
	) (profilerevidence.VerifiedIdleEvidence, error)
}

// CurveDeriver assembles retained Completed Run evidence into the minimum-energy
// objective decision. It retains no evidence between calls.
type CurveDeriver struct {
	evidence CurveEvidenceLoader
}

func NewCurveDeriver(evidence CurveEvidenceLoader) (*CurveDeriver, error) {
	if evidence == nil {
		return nil, errors.New("curve evidence loader are required")
	}
	return &CurveDeriver{evidence: evidence}, nil
}

// Derive evaluates the completed Runs bound by a caller's per-reconcile
// CurveSource closure. The requested batches must be the recorded plan prefix.
func (d *CurveDeriver) Derive(
	ctx context.Context,
	plan profilederivation.CurvePlan,
	activeRuns, idleRuns []edgev1alpha1.ProfilingRun,
	batches []int,
	policy profilederivation.EnergyObjectivePolicy,
) (batchenergyoptimization.CurveResult, error) {
	if err := validateCurvePrefix(plan.PlannedBatchSizes(), batches); err != nil {
		return batchenergyoptimization.CurveResult{}, err
	}
	pointPolicy := profilederivation.PointPolicy{
		Version:         energyObjectivePointPolicyVersion,
		MinimumTrials:   policy.TrialsPerBatch,
		ConfidenceLevel: policy.ConfidenceLevel,
	}

	passBatches, activeEvidence, err := d.collectActiveTrials(ctx, activeRuns, batches)
	if err != nil {
		return batchenergyoptimization.CurveResult{}, err
	}
	idleTrials, idleEvidence, err := d.collectIdleTrials(ctx, idleRuns)
	if err != nil {
		return batchenergyoptimization.CurveResult{}, err
	}

	if len(activeEvidence) == 0 || len(idleEvidence) == 0 {
		return batchenergyoptimization.CurveResult{}, errors.New("curve requires active and idle evidence")
	}
	var state profilederivation.VerifiedCurveState
	for index, active := range activeEvidence {
		projected, err := profilerprojection.ProjectVerifiedCurveState(plan, active, idleEvidence[0])
		if err != nil {
			return batchenergyoptimization.CurveResult{}, fmt.Errorf("project active curve condition: %w", err)
		}
		if index == 0 {
			state = projected
		}
	}
	for _, idle := range idleEvidence[1:] {
		if _, err := profilerprojection.ProjectVerifiedCurveState(plan, activeEvidence[0], idle); err != nil {
			return batchenergyoptimization.CurveResult{}, fmt.Errorf("project idle curve condition: %w", err)
		}
	}
	baseline, err := profilederivation.DeriveIdleBaseline(idleTrials, pointPolicy)
	if err != nil {
		return batchenergyoptimization.CurveResult{}, fmt.Errorf("derive idle baseline: %w", err)
	}
	verifiedPlan, err := profilederivation.VerifyCurvePlan(plan, state)
	if err != nil {
		return batchenergyoptimization.CurveResult{}, fmt.Errorf("verify curve plan: %w", err)
	}
	curve, err := profilederivation.DeriveCurve(
		[]profilederivation.Pass{{Role: profilederivation.PassSearch, Batches: passBatches}},
		verifiedPlan,
		map[string]profilederivation.IdleBaseline{baseline.StateIdentity: baseline},
		pointPolicy,
	)
	if err != nil {
		return batchenergyoptimization.CurveResult{}, fmt.Errorf("derive energy-search curve: %w", err)
	}
	result, err := profilederivation.DeriveMinimumEnergyBatch(curve, policy)
	if err != nil {
		return batchenergyoptimization.CurveResult{}, fmt.Errorf("derive minimum-energy batch: %w", err)
	}
	return projectEnergyOptimizationResult(result)
}

func (d *CurveDeriver) collectActiveTrials(
	ctx context.Context,
	runs []edgev1alpha1.ProfilingRun,
	batches []int,
) ([]profilederivation.PassBatch, []profilerevidence.VerifiedEvidence, error) {
	passBatches := make([]profilederivation.PassBatch, len(batches))
	batchIndexes := make(map[int]int, len(batches))
	for index, batch := range batches {
		passBatches[index] = profilederivation.PassBatch{Batch: batch}
		batchIndexes[batch] = index
	}

	evidence := make([]profilerevidence.VerifiedEvidence, 0, len(runs))
	for index := range runs {
		run := &runs[index]
		batchIndex, exists := batchIndexes[int(run.Spec.Measurement.Batch)]
		if !exists {
			return nil, nil, fmt.Errorf(
				"ProfilingRun %q batch %d is outside the requested curve prefix",
				run.Name, run.Spec.Measurement.Batch,
			)
		}
		verified, err := d.loadActive(ctx, run)
		if err != nil {
			return nil, nil, err
		}
		trial, err := profilerprojection.Trial(verified)
		if err != nil {
			return nil, nil, fmt.Errorf("evaluate ProfilingRun %q: %w", run.Name, err)
		}
		passBatches[batchIndex].Trials = append(passBatches[batchIndex].Trials, trial)
		evidence = append(evidence, verified)
	}
	return passBatches, evidence, nil
}

func (d *CurveDeriver) collectIdleTrials(
	ctx context.Context,
	runs []edgev1alpha1.ProfilingRun,
) ([]profilederivation.IdleTrial, []profilerevidence.VerifiedIdleEvidence, error) {
	trials := make([]profilederivation.IdleTrial, 0, len(runs))
	evidence := make([]profilerevidence.VerifiedIdleEvidence, 0, len(runs))
	for index := range runs {
		run := &runs[index]
		verified, err := d.loadIdle(ctx, run)
		if err != nil {
			return nil, nil, err
		}
		evidence = append(evidence, verified)
		trial, err := profilerprojection.IdleTrial(verified, evidence[0])
		if err != nil {
			return nil, nil, fmt.Errorf("evaluate ProfilingRun %q: %w", run.Name, err)
		}
		trials = append(trials, trial)
	}
	return trials, evidence, nil
}

func (d *CurveDeriver) loadActive(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (profilerevidence.VerifiedEvidence, error) {
	reference, err := curveEvidenceIdentity(run)
	if err != nil {
		return profilerevidence.VerifiedEvidence{}, err
	}
	verified, err := d.evidence.Load(ctx, run, reference)
	if err != nil {
		return profilerevidence.VerifiedEvidence{}, fmt.Errorf(
			"load ProfilingRun %q retained evidence: %w", run.Name, err,
		)
	}
	return verified, nil
}

func (d *CurveDeriver) loadIdle(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (profilerevidence.VerifiedIdleEvidence, error) {
	reference, err := curveEvidenceIdentity(run)
	if err != nil {
		return profilerevidence.VerifiedIdleEvidence{}, err
	}
	verified, err := d.evidence.LoadIdle(ctx, run, reference)
	if err != nil {
		return profilerevidence.VerifiedIdleEvidence{}, fmt.Errorf(
			"load ProfilingRun %q retained idle evidence: %w", run.Name, err,
		)
	}
	return verified, nil
}

func validateCurvePrefix(planned, batches []int) error {
	if len(batches) == 0 || len(batches) > len(planned) {
		return errors.New("requested curve batches must be a non-empty contiguous prefix of the recorded plan")
	}
	for index, batch := range batches {
		if batch != planned[index] {
			return errors.New("requested curve batches must be a non-empty contiguous prefix of the recorded plan")
		}
	}
	return nil
}

func curveEvidenceIdentity(run *edgev1alpha1.ProfilingRun) (evidencestore.ObjectIdentity, error) {
	if run.Status.Evidence == nil {
		return evidencestore.ObjectIdentity{}, fmt.Errorf("ProfilingRun %q has no verified evidence reference", run.Name)
	}
	reference := evidencestore.ObjectIdentity{
		Store: run.Status.Evidence.Store,
		Key:   run.Status.Evidence.Key,
		Size:  run.Status.Evidence.Size,
	}
	if err := reference.Validate(); err != nil {
		return evidencestore.ObjectIdentity{}, fmt.Errorf("ProfilingRun %q evidence reference: %w", run.Name, err)
	}
	return reference, nil
}

func projectEnergyOptimizationResult(
	result profilederivation.EnergyOptimizationResult,
) (batchenergyoptimization.CurveResult, error) {
	if result.BestBatch == nil {
		return batchenergyoptimization.CurveResult{}, errors.New("energy-optimization result omitted its best batch")
	}
	projected := batchenergyoptimization.CurveResult{
		Detail: result,
	}
	switch result.Status {
	case profilederivation.EnergySearchContinue:
		projected.Continue = true
	case profilederivation.EnergySearchComplete:
		switch result.CompletionReason {
		case profilederivation.EnergySearchLocalMinimum:
			projected.Outcome = energyLocalMinimumOutcome
		case profilederivation.EnergySearchSpaceExhausted:
			projected.Outcome = energySearchExhaustedOutcome
		default:
			return batchenergyoptimization.CurveResult{}, fmt.Errorf(
				"energy-optimization result has unsupported completion reason %q",
				result.CompletionReason,
			)
		}
	default:
		return batchenergyoptimization.CurveResult{}, fmt.Errorf(
			"energy-optimization result has unsupported status %q", result.Status,
		)
	}
	return projected, nil
}
