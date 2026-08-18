package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profilederivation"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
)

// The point-policy version and confidence level stay paired so the version
// identifies the complete numeric rule. Changing the level requires changing
// the version in the same edit.
const (
	sequentialPointPolicyVersion   = "sequential-point-v1"
	sequentialPointConfidenceLevel = 0.95
)

const (
	sequentialNoFurtherImprovementOutcome = "Stopped/" + string(profilederivation.StopNoFurtherImprovement)
	sequentialEnergyDivergenceOutcome     = "Stopped/" + string(profilederivation.StopEnergyDivergence)
)

// CurveIntentLoader retrieves the immutable inputs frozen for one Run.
type CurveIntentLoader interface {
	Load(context.Context, *edgev1alpha1.ProfilingRun) (profilercontroller.FrozenRun, error)
}

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

// CurveDeriver assembles retained Accepted Run evidence into the existing
// sequential curve decision. It retains no evidence between calls.
type CurveDeriver struct {
	intents  CurveIntentLoader
	evidence CurveEvidenceLoader
}

func NewCurveDeriver(intents CurveIntentLoader, evidence CurveEvidenceLoader) (*CurveDeriver, error) {
	if intents == nil || evidence == nil {
		return nil, errors.New("curve intent and evidence loaders are required")
	}
	return &CurveDeriver{intents: intents, evidence: evidence}, nil
}

// Derive evaluates the accepted Runs bound by a caller's per-reconcile
// CurveSource closure. The requested batches must be the recorded plan prefix.
func (d *CurveDeriver) Derive(
	ctx context.Context,
	plan profilederivation.CurvePlan,
	activeRuns, idleRuns []edgev1alpha1.ProfilingRun,
	batches []int,
	sequentialPolicy profilederivation.SequentialPolicy,
) (batchsaturation.CurveResult, error) {
	if err := validateCurvePrefix(plan.PlannedBatchSizes(), batches); err != nil {
		return batchsaturation.CurveResult{}, err
	}
	pointPolicy := profilederivation.PointPolicy{
		Version:         sequentialPointPolicyVersion,
		MinimumTrials:   sequentialPolicy.TrialsPerBatch,
		ConfidenceLevel: sequentialPointConfidenceLevel,
	}

	passBatches := make([]profilederivation.PassBatch, len(batches))
	batchIndexes := make(map[int]int, len(batches))
	for index, batch := range batches {
		passBatches[index] = profilederivation.PassBatch{Batch: batch}
		batchIndexes[batch] = index
	}

	var activeWitness profilerevidence.VerifiedEvidence
	for index := range activeRuns {
		run := &activeRuns[index]
		batchIndex, exists := batchIndexes[int(run.Spec.Measurement.Batch)]
		if !exists {
			return batchsaturation.CurveResult{}, fmt.Errorf(
				"ProfilingRun %q batch %d is outside the requested curve prefix",
				run.Name, run.Spec.Measurement.Batch,
			)
		}
		frozen, verified, err := d.loadActive(ctx, run)
		if err != nil {
			return batchsaturation.CurveResult{}, err
		}
		result, admission, err := profileradmission.EvaluateTrialAdmission(verified, frozen.AdmissionPolicy)
		if err != nil {
			return batchsaturation.CurveResult{}, fmt.Errorf("evaluate ProfilingRun %q: %w", run.Name, err)
		}
		if admission == nil {
			return batchsaturation.CurveResult{}, fmt.Errorf(
				"ProfilingRun %q was presented as Accepted but evaluated as %q", run.Name, result.Verdict,
			)
		}
		passBatches[batchIndex].Admissions = append(passBatches[batchIndex].Admissions, *admission)
		if index == 0 {
			activeWitness = verified
		}
	}

	idleTrials := make([]profilederivation.IdleTrial, 0, len(idleRuns))
	var idleWitness profilerevidence.VerifiedIdleEvidence
	for index := range idleRuns {
		run := &idleRuns[index]
		frozen, verified, err := d.loadIdle(ctx, run)
		if err != nil {
			return batchsaturation.CurveResult{}, err
		}
		result, trial, err := profileradmission.EvaluateIdle(verified, frozen.AdmissionPolicy)
		if err != nil {
			return batchsaturation.CurveResult{}, fmt.Errorf("evaluate ProfilingRun %q: %w", run.Name, err)
		}
		if trial == nil {
			return batchsaturation.CurveResult{}, fmt.Errorf(
				"ProfilingRun %q was presented as Accepted but evaluated as %q", run.Name, result.Verdict,
			)
		}
		idleTrials = append(idleTrials, *trial)
		if index == 0 {
			idleWitness = verified
		}
	}

	baseline, err := profilederivation.DeriveIdleBaseline(idleTrials, pointPolicy)
	if err != nil {
		return batchsaturation.CurveResult{}, fmt.Errorf("derive idle baseline: %w", err)
	}
	state, err := profileradmission.ProjectVerifiedCurveState(plan, activeWitness, idleWitness)
	if err != nil {
		return batchsaturation.CurveResult{}, fmt.Errorf("project verified curve state: %w", err)
	}
	verifiedPlan, err := profilederivation.VerifyCurvePlan(plan, state)
	if err != nil {
		return batchsaturation.CurveResult{}, fmt.Errorf("verify curve plan: %w", err)
	}
	curve, err := profilederivation.DeriveCurve(
		[]profilederivation.Pass{{Role: profilederivation.PassDiscovery, Batches: passBatches}},
		verifiedPlan,
		map[string]profilederivation.IdleBaseline{baseline.StateIdentity: baseline},
		pointPolicy,
	)
	if err != nil {
		return batchsaturation.CurveResult{}, fmt.Errorf("derive discovery curve: %w", err)
	}
	result, err := profilederivation.DeriveSequentialBatchDecision(curve, sequentialPolicy)
	if err != nil {
		return batchsaturation.CurveResult{}, fmt.Errorf("derive sequential batch decision: %w", err)
	}
	return projectSequentialCurveResult(result)
}

func (d *CurveDeriver) loadActive(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (profilercontroller.FrozenRun, profilerevidence.VerifiedEvidence, error) {
	frozen, err := d.intents.Load(ctx, run)
	if err != nil {
		return profilercontroller.FrozenRun{}, profilerevidence.VerifiedEvidence{}, fmt.Errorf(
			"load ProfilingRun %q frozen intent: %w", run.Name, err,
		)
	}
	reference, err := curveEvidenceIdentity(run)
	if err != nil {
		return profilercontroller.FrozenRun{}, profilerevidence.VerifiedEvidence{}, err
	}
	verified, err := d.evidence.Load(ctx, run, reference)
	if err != nil {
		return profilercontroller.FrozenRun{}, profilerevidence.VerifiedEvidence{}, fmt.Errorf(
			"load ProfilingRun %q retained evidence: %w", run.Name, err,
		)
	}
	return frozen, verified, nil
}

func (d *CurveDeriver) loadIdle(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (profilercontroller.FrozenRun, profilerevidence.VerifiedIdleEvidence, error) {
	frozen, err := d.intents.Load(ctx, run)
	if err != nil {
		return profilercontroller.FrozenRun{}, profilerevidence.VerifiedIdleEvidence{}, fmt.Errorf(
			"load ProfilingRun %q frozen intent: %w", run.Name, err,
		)
	}
	reference, err := curveEvidenceIdentity(run)
	if err != nil {
		return profilercontroller.FrozenRun{}, profilerevidence.VerifiedIdleEvidence{}, err
	}
	verified, err := d.evidence.LoadIdle(ctx, run, reference)
	if err != nil {
		return profilercontroller.FrozenRun{}, profilerevidence.VerifiedIdleEvidence{}, fmt.Errorf(
			"load ProfilingRun %q retained idle evidence: %w", run.Name, err,
		)
	}
	return frozen, verified, nil
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

func projectSequentialCurveResult(
	result profilederivation.SequentialResult,
) (batchsaturation.CurveResult, error) {
	if result.SelectedBatch == nil {
		return batchsaturation.CurveResult{}, errors.New("sequential result omitted its selected batch")
	}
	projected := batchsaturation.CurveResult{
		SelectedBatch: *result.SelectedBatch,
		Detail:        result,
	}
	switch result.Status {
	case profilederivation.SequentialContinue:
		projected.Continue = true
	case profilederivation.SequentialStopped:
		switch result.StopReason {
		case profilederivation.StopNoFurtherImprovement:
			projected.Outcome = sequentialNoFurtherImprovementOutcome
		case profilederivation.StopCapabilityBoundary:
			return batchsaturation.CurveResult{}, errors.New(
				"sequential derivation returned an unreachable capability boundary",
			)
		case profilederivation.StopEnergyDivergence:
			projected.Outcome = sequentialEnergyDivergenceOutcome
		default:
			return batchsaturation.CurveResult{}, fmt.Errorf(
				"sequential result has unsupported stop reason %q", result.StopReason,
			)
		}
	default:
		return batchsaturation.CurveResult{}, fmt.Errorf(
			"sequential result has unsupported status %q", result.Status,
		)
	}
	return projected, nil
}
