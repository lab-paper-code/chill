package controller

import (
	"context"
	"errors"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profileradmission "github.com/lab-paper-code/chill/internal/profiling/profiler/admission"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RunEvidenceAdmitter interface {
	Admit(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		ResolvedRun,
		*batchv1.Job,
		evidencestore.ObjectIdentity,
	) (profileradmission.Result, error)
}

// KubernetesEvidenceAdmitter resolves only the retained store binding. Exact
// evidence loading and scientific evaluation remain in Kubernetes-free packages.
type KubernetesEvidenceAdmitter struct {
	loader *evidencekube.RetainedLoader
}

// NewKubernetesEvidenceAdmitterWithRetainedLoader reuses a composition-root
// loader whose Run-binding checks are also needed by aggregate derivation.
func NewKubernetesEvidenceAdmitterWithRetainedLoader(
	loader *evidencekube.RetainedLoader,
) (*KubernetesEvidenceAdmitter, error) {
	if loader == nil {
		return nil, errors.New("retained evidence loader is required")
	}
	return &KubernetesEvidenceAdmitter{loader: loader}, nil
}

func (a *KubernetesEvidenceAdmitter) Admit(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	resolved ResolvedRun,
	job *batchv1.Job,
	reference evidencestore.ObjectIdentity,
) (profileradmission.Result, error) {
	if run == nil || job == nil {
		return profileradmission.Result{}, errors.New("ProfilingRun and measurement Job are required")
	}
	if err := validateResolvedRunForRun(run, resolved); err != nil {
		return profileradmission.Result{}, err
	}
	prepared := resolved.Prepared
	if run.Status.Job == nil || run.Status.Job.Name != job.Name || run.Status.Job.UID != string(job.UID) ||
		!metav1.IsControlledBy(job, run) {
		return profileradmission.Result{}, errors.New("measurement Job does not match the recorded Run execution")
	}
	if prepared.RunConfig.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		verified, err := a.loader.LoadIdle(ctx, run, reference)
		if err != nil {
			return profileradmission.Result{}, err
		}
		result, _, err := profileradmission.EvaluateIdle(verified.ForAdmission(), resolved.AdmissionPolicy)
		return result, err
	}
	verified, err := a.loader.Load(ctx, run, reference)
	if err != nil {
		return profileradmission.Result{}, err
	}
	return profileradmission.Evaluate(verified.ForAdmission(), resolved.AdmissionPolicy)
}
