package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RunEvidenceAdmitter interface {
	Admit(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		profiler.PreparedRun,
		*batchv1.Job,
		evidencestore.ObjectIdentity,
	) (profileradmission.Result, error)
}

// KubernetesEvidenceAdmitter resolves only the retained store binding. Exact
// evidence loading and scientific evaluation remain in Kubernetes-free packages.
type KubernetesEvidenceAdmitter struct {
	reader       client.Reader
	storeFactory EvidenceObjectStoreFactory
}

func NewKubernetesEvidenceAdmitter(
	reader client.Reader,
	storeFactory EvidenceObjectStoreFactory,
) (*KubernetesEvidenceAdmitter, error) {
	if reader == nil || storeFactory == nil {
		return nil, errors.New("Kubernetes API reader and evidence object-store factory are required")
	}
	return &KubernetesEvidenceAdmitter{reader: reader, storeFactory: storeFactory}, nil
}

func (a *KubernetesEvidenceAdmitter) Admit(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
	job *batchv1.Job,
	reference evidencestore.ObjectIdentity,
) (profileradmission.Result, error) {
	if run == nil || job == nil {
		return profileradmission.Result{}, errors.New("ProfilingRun and measurement Job are required")
	}
	if _, err := validatePreparedForRun(run, prepared); err != nil {
		return profileradmission.Result{}, err
	}
	if run.Status.Job == nil || run.Status.Job.Name != job.Name || run.Status.Job.UID != string(job.UID) ||
		!metav1.IsControlledBy(job, run) {
		return profileradmission.Result{}, errors.New("measurement Job does not match the recorded Run execution")
	}
	binding, err := executionJobEvidenceStoreBinding(job)
	if err != nil {
		return profileradmission.Result{}, err
	}
	credentials, err := loadEvidenceStoreCredentials(ctx, a.reader, run.Namespace, binding.CredentialsSecretName)
	if err != nil {
		return profileradmission.Result{}, err
	}
	store, err := a.storeFactory(binding, credentials.AccessKeyID, credentials.SecretAccessKey)
	if err != nil {
		return profileradmission.Result{}, fmt.Errorf("construct retained evidence reader: %w", err)
	}
	loader, err := profilerevidence.NewLoader(store, maxFinalizationObjectBytes)
	if err != nil {
		return profileradmission.Result{}, fmt.Errorf("construct verified evidence loader: %w", err)
	}
	execution, raw, err := loader.Load(ctx, reference)
	if err != nil {
		return profileradmission.Result{}, err
	}
	if execution.IntentDigest != prepared.IntentDigest || execution.Terminal.Job.UID != string(job.UID) {
		return profileradmission.Result{}, errors.New("retained evidence does not belong to the recorded Run")
	}
	if run.Status.ActualExecution == nil ||
		run.Status.ActualExecution.Pod.UID != execution.Terminal.Pod.UID ||
		run.Status.ActualExecution.Node.UID != execution.Terminal.Node.UID {
		return profileradmission.Result{}, errors.New("retained evidence contradicts recorded execution identities")
	}
	return profileradmission.Evaluate(execution, raw, run.Spec.AdmissionPolicy)
}
