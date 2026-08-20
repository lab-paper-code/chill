package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
	profileradmission "github.com/lab-paper-code/chill/internal/profiling/profiler/admission"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
)

type IntentLoader interface {
	Load(context.Context, *edgev1alpha1.ProfilingRun) (profilercontroller.FrozenRun, error)
}

type EvidenceLoader interface {
	LoadIdle(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		evidencestore.ObjectIdentity,
	) (profilerevidence.VerifiedIdleEvidence, error)
}

type Deriver struct {
	intents  IntentLoader
	evidence EvidenceLoader
}

func NewDeriver(intents IntentLoader, evidence EvidenceLoader) (*Deriver, error) {
	if intents == nil || evidence == nil {
		return nil, errors.New("idle baseline intent and evidence loaders are required")
	}
	return &Deriver{intents: intents, evidence: evidence}, nil
}

func (d *Deriver) Derive(
	ctx context.Context,
	runs []edgev1alpha1.ProfilingRun,
	policy profilederivation.PointPolicy,
) (profilederivation.IdleBaseline, idlebaseline.AcceptedFinalEvidence, error) {
	trials := make([]profilederivation.IdleTrial, 0, len(runs))
	references := make(idlebaseline.AcceptedFinalEvidence, len(runs))
	for index := range runs {
		run := &runs[index]
		if run.Status.Admission == nil || run.Status.Admission.Verdict != string(profileradmission.VerdictAccepted) ||
			run.Status.Evidence == nil {
			return profilederivation.IdleBaseline{}, nil,
				fmt.Errorf("ProfilingRun %q is not an Accepted retained idle execution", run.Name)
		}
		frozen, err := d.intents.Load(ctx, run)
		if err != nil {
			return profilederivation.IdleBaseline{}, nil, fmt.Errorf("load ProfilingRun %q frozen intent: %w", run.Name, err)
		}
		identity := evidencestore.ObjectIdentity{
			Store: run.Status.Evidence.Store, Key: run.Status.Evidence.Key, Size: run.Status.Evidence.Size,
		}
		verified, err := d.evidence.LoadIdle(ctx, run, identity)
		if err != nil {
			return profilederivation.IdleBaseline{}, nil, fmt.Errorf("load ProfilingRun %q idle evidence: %w", run.Name, err)
		}
		result, trial, err := profileradmission.EvaluateIdle(verified, frozen.AdmissionPolicy)
		if err != nil {
			return profilederivation.IdleBaseline{}, nil, fmt.Errorf("evaluate ProfilingRun %q: %w", run.Name, err)
		}
		if trial == nil {
			return profilederivation.IdleBaseline{}, nil,
				fmt.Errorf("ProfilingRun %q status is Accepted but retained evidence evaluates as %q", run.Name, result.Verdict)
		}
		trials = append(trials, *trial)
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
