package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	evidencekube "github.com/lab-paper-code/chill/internal/profiler/evidence/kube"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RunEvidenceAdmitter interface {
	Admit(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		FrozenRun,
		*batchv1.Job,
		evidencestore.ObjectIdentity,
	) (profileradmission.Result, error)
}

// KubernetesEvidenceAdmitter resolves only the retained store binding. Exact
// evidence loading and scientific evaluation remain in Kubernetes-free packages.
type KubernetesEvidenceAdmitter struct {
	loader *evidencekube.RetainedLoader
}

func NewKubernetesEvidenceAdmitter(
	reader client.Reader,
	storeFactory EvidenceObjectStoreFactory,
) (*KubernetesEvidenceAdmitter, error) {
	if reader == nil || storeFactory == nil {
		return nil, errors.New("Kubernetes API reader and evidence object-store factory are required")
	}
	loader, err := evidencekube.NewRetainedLoader(
		reader,
		func(binding storekube.Binding, credentials storekube.Credentials) (evidencestore.LocationReader, error) {
			return storeFactory(binding, credentials.AccessKeyID, credentials.SecretAccessKey)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct retained evidence loader: %w", err)
	}
	return NewKubernetesEvidenceAdmitterWithRetainedLoader(loader)
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
	frozen FrozenRun,
	job *batchv1.Job,
	reference evidencestore.ObjectIdentity,
) (profileradmission.Result, error) {
	if run == nil || job == nil {
		return profileradmission.Result{}, errors.New("ProfilingRun and measurement Job are required")
	}
	if _, err := validateFrozenRunForRun(run, frozen); err != nil {
		return profileradmission.Result{}, err
	}
	prepared := frozen.Prepared
	if run.Status.Job == nil || run.Status.Job.Name != job.Name || run.Status.Job.UID != string(job.UID) ||
		!metav1.IsControlledBy(job, run) {
		return profileradmission.Result{}, errors.New("measurement Job does not match the recorded Run execution")
	}
	if prepared.Intent.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		verified, err := a.loader.LoadIdle(ctx, run, reference)
		if err != nil {
			return profileradmission.Result{}, err
		}
		result, _, err := profileradmission.EvaluateIdle(verified.ForAdmission(), frozen.AdmissionPolicy)
		return result, err
	}
	verified, err := a.loader.Load(ctx, run, reference)
	if err != nil {
		return profileradmission.Result{}, err
	}
	return profileradmission.Evaluate(verified.ForAdmission(), frozen.AdmissionPolicy)
}
