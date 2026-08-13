package kube

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultMaximumObjectBytes int64 = 64 << 20

type RetainedStoreFactory func(
	storekube.Binding,
	storekube.Credentials,
) (evidencestore.LocationReader, error)

// RetainedLoader reconstructs the Evidence Store connection frozen into one
// Run's owned Job, then independently verifies the referenced immutable
// evidence. It never resolves the current store configuration as a substitute.
type RetainedLoader struct {
	reader         client.Reader
	stores         RetainedStoreFactory
	maxObjectBytes int64
}

// RetainedActiveEvidence proves that the Evidence Store objects and their
// recorded ProfilingRun binding crossed the RetainedLoader boundary together.
// Its zero value is invalid; only RetainedLoader can construct a usable value.
type RetainedActiveEvidence struct {
	verified profilerevidence.VerifiedEvidence
}

func (e RetainedActiveEvidence) Records() (
	profilerevidence.ExecutionEvidence,
	profilerevidence.RawMeasurement,
	error,
) {
	return e.verified.Records()
}

func (e RetainedActiveEvidence) ForAdmission() profilerevidence.VerifiedEvidence {
	return e.verified
}

// RetainedIdleEvidence is the idle counterpart to RetainedActiveEvidence.
type RetainedIdleEvidence struct {
	verified profilerevidence.VerifiedIdleEvidence
}

func (e RetainedIdleEvidence) ForAdmission() profilerevidence.VerifiedIdleEvidence {
	return e.verified
}

func NewRetainedLoader(reader client.Reader, stores RetainedStoreFactory) (*RetainedLoader, error) {
	if reader == nil || stores == nil {
		return nil, errors.New("Kubernetes API reader and retained Evidence Store factory are required")
	}
	return &RetainedLoader{reader: reader, stores: stores, maxObjectBytes: defaultMaximumObjectBytes}, nil
}

func (l *RetainedLoader) Load(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reference evidencestore.ObjectIdentity,
) (RetainedActiveEvidence, error) {
	loader, err := l.loaderFor(ctx, run, reference)
	if err != nil {
		return RetainedActiveEvidence{}, err
	}
	verified, err := loader.Load(ctx, reference)
	if err != nil {
		return RetainedActiveEvidence{}, err
	}
	execution, _, err := verified.Records()
	if err != nil {
		return RetainedActiveEvidence{}, err
	}
	if err := validateRunEvidenceBinding(run, execution); err != nil {
		return RetainedActiveEvidence{}, err
	}
	return RetainedActiveEvidence{verified: verified}, nil
}

func (l *RetainedLoader) LoadIdle(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reference evidencestore.ObjectIdentity,
) (RetainedIdleEvidence, error) {
	loader, err := l.loaderFor(ctx, run, reference)
	if err != nil {
		return RetainedIdleEvidence{}, err
	}
	verified, err := loader.LoadIdle(ctx, reference)
	if err != nil {
		return RetainedIdleEvidence{}, err
	}
	execution, _, err := verified.Records()
	if err != nil {
		return RetainedIdleEvidence{}, err
	}
	if err := validateRunEvidenceBinding(run, execution); err != nil {
		return RetainedIdleEvidence{}, err
	}
	return RetainedIdleEvidence{verified: verified}, nil
}

func (l *RetainedLoader) loaderFor(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reference evidencestore.ObjectIdentity,
) (*profilerevidence.Loader, error) {
	if run == nil || run.Namespace == "" || run.Status.Job == nil {
		return nil, errors.New("ProfilingRun with a recorded Job is required")
	}
	job := &batchv1.Job{}
	key := types.NamespacedName{Namespace: run.Namespace, Name: run.Status.Job.Name}
	if err := l.reader.Get(ctx, key, job); err != nil {
		return nil, fmt.Errorf("get recorded measurement Job %s: %w", key, err)
	}
	if string(job.UID) != run.Status.Job.UID || !metav1.IsControlledBy(job, run) {
		return nil, errors.New("measurement Job does not match the recorded Run execution")
	}
	binding, err := storekube.BindingFromJob(job, "runtime")
	if err != nil {
		return nil, err
	}
	if reference.Store != binding.Store {
		return nil, errors.New("evidence reference store does not match the retained Job binding")
	}
	credentials, err := storekube.LoadCredentials(ctx, l.reader, run.Namespace, binding.CredentialsSecretName)
	if err != nil {
		return nil, err
	}
	store, err := l.stores(binding, credentials)
	if err != nil {
		return nil, fmt.Errorf("construct retained evidence reader: %w", err)
	}
	loader, err := profilerevidence.NewLoader(store, l.maxObjectBytes)
	if err != nil {
		return nil, fmt.Errorf("construct verified evidence loader: %w", err)
	}
	return loader, nil
}

func validateRunEvidenceBinding(
	run *edgev1alpha1.ProfilingRun,
	execution profilerevidence.ExecutionEvidence,
) error {
	if run.Status.Evidence == nil || run.Status.Evidence.SchemaVersion != execution.SchemaVersion ||
		execution.Terminal.Job.Namespace != run.Namespace ||
		execution.Terminal.Job.Name != run.Status.Job.Name ||
		execution.Terminal.Job.UID != run.Status.Job.UID {
		return errors.New("retained evidence does not belong to the recorded Run")
	}
	if run.Status.ActualExecution == nil ||
		execution.Terminal.Pod.Namespace != run.Namespace ||
		execution.Terminal.Pod.Name != run.Status.ActualExecution.Pod.Name ||
		execution.Terminal.Pod.UID != run.Status.ActualExecution.Pod.UID ||
		execution.Terminal.Node.Name != run.Status.ActualExecution.Node.Name ||
		execution.Terminal.Node.UID != run.Status.ActualExecution.Node.UID {
		return errors.New("retained evidence contradicts recorded execution identities")
	}
	if execution.Intent.Actuation == nil {
		if run.Status.Actuation != nil || execution.Actuation != nil {
			return errors.New("fixed retained evidence has unexpected actuation identity")
		}
		return nil
	}
	if run.Status.Actuation == nil || execution.Actuation == nil ||
		execution.Actuation.Run.Namespace != run.Namespace ||
		execution.Actuation.Run.Name != run.Name ||
		execution.Actuation.Run.UID != string(run.UID) ||
		execution.Actuation.Handoff.Namespace != run.Namespace ||
		execution.Actuation.Handoff.Name != run.Status.Actuation.Name ||
		execution.Actuation.Handoff.UID != run.Status.Actuation.UID {
		return errors.New("retained actuation evidence does not match the recorded confirming transition")
	}
	return nil
}
