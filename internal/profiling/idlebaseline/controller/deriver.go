package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"

	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
)

type EvidenceLoader interface {
	LoadIdle(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		evidencestore.ObjectIdentity,
	) (profilerevidence.VerifiedIdleEvidence, error)
}

type Deriver struct {
	evidence EvidenceLoader
}

func NewDeriver(evidence EvidenceLoader) (*Deriver, error) {
	if evidence == nil {
		return nil, errors.New("idle baseline evidence loader are required")
	}
	return &Deriver{evidence: evidence}, nil
}

func (d *Deriver) Derive(
	ctx context.Context,
	runs []edgev1alpha1.ProfilingRun,
	policy profilederivation.PointPolicy,
) (profilederivation.IdleBaseline, idlebaseline.FinalEvidence, error) {
	trials := make([]profilederivation.IdleTrial, 0, len(runs))
	references := make(idlebaseline.FinalEvidence, len(runs))
	var reference profilerevidence.VerifiedIdleEvidence
	for index := range runs {
		run := &runs[index]
		if run.Status.Evidence == nil {
			return profilederivation.IdleBaseline{}, nil,
				fmt.Errorf("ProfilingRun %q is not an Accepted retained idle execution", run.Name)
		}
		identity := evidencestore.ObjectIdentity{
			Store: run.Status.Evidence.Store, Key: run.Status.Evidence.Key, Size: run.Status.Evidence.Size,
		}
		verified, err := d.evidence.LoadIdle(ctx, run, identity)
		if err != nil {
			return profilederivation.IdleBaseline{}, nil, fmt.Errorf("load ProfilingRun %q idle evidence: %w", run.Name, err)
		}
		if index == 0 {
			reference = verified
		}
		trial, err := profilerprojection.IdleTrial(verified, reference)
		if err != nil {
			return profilederivation.IdleBaseline{}, nil, fmt.Errorf("evaluate ProfilingRun %q: %w", run.Name, err)
		}
		trials = append(trials, trial)
		references[trial.TrialID] = idlebaseline.StoredObjectReference{
			Store: run.Status.Evidence.Store, Key: run.Status.Evidence.Key,
			SchemaVersion: run.Status.Evidence.SchemaVersion, Size: run.Status.Evidence.Size,
		}
	}
	baseline, err := profilederivation.DeriveIdleBaseline(trials, policy)
	if err != nil {
		return profilederivation.IdleBaseline{}, nil, fmt.Errorf("derive model-loaded idle baseline: %w", err)
	}
	return baseline, references, nil
}
