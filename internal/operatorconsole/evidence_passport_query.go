package operatorconsole

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VerifiedEvidenceLoader retrieves and independently verifies the finalized
// evidence and raw measurement named by an immutable object identity.
type VerifiedEvidenceLoader interface {
	Load(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		evidencestore.ObjectIdentity,
	) (profilerevidence.ExecutionEvidence, profilerevidence.RawMeasurement, error)
}

// EvidencePassportSnapshot joins one ProfilingRun control-plane record with
// the verified immutable evidence it references. It is a read-time projection,
// not a second persisted copy of either source.
type EvidencePassportSnapshot struct {
	ReadAt      time.Time
	Run         edgev1alpha1.ProfilingRun
	Execution   profilerevidence.ExecutionEvidence
	Measurement profilerevidence.RawMeasurement
}

// KubernetesEvidencePassportQuery exact-reads a ProfilingRun, follows only its
// status evidence reference, and rejects evidence that contradicts the Run's
// recorded execution identities.
type KubernetesEvidencePassportQuery struct {
	reader client.Reader
	loader VerifiedEvidenceLoader
	now    func() time.Time
}

func NewKubernetesEvidencePassportQuery(
	reader client.Reader,
	loader VerifiedEvidenceLoader,
) (*KubernetesEvidencePassportQuery, error) {
	if reader == nil || loader == nil {
		return nil, errors.New("Kubernetes ProfilingRun reader and verified evidence loader are required")
	}
	return &KubernetesEvidencePassportQuery{reader: reader, loader: loader, now: time.Now}, nil
}

func (q *KubernetesEvidencePassportQuery) Read(
	ctx context.Context,
	namespace, name string,
) (EvidencePassportSnapshot, error) {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(namespace) != namespace ||
		strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return EvidencePassportSnapshot{}, errors.New("ProfilingRun namespace and name are required without surrounding whitespace")
	}

	run := &edgev1alpha1.ProfilingRun{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := q.reader.Get(ctx, key, run); err != nil {
		return EvidencePassportSnapshot{}, fmt.Errorf("get ProfilingRun %s: %w", key, err)
	}
	reference, err := evidenceIdentity(run.Status.Evidence)
	if err != nil {
		return EvidencePassportSnapshot{}, fmt.Errorf("read ProfilingRun %s evidence reference: %w", key, err)
	}
	execution, measurement, err := q.loader.Load(ctx, run, reference)
	if err != nil {
		return EvidencePassportSnapshot{}, fmt.Errorf("load ProfilingRun %s evidence: %w", key, err)
	}
	if err := validatePassportBinding(run, execution); err != nil {
		return EvidencePassportSnapshot{}, fmt.Errorf("validate ProfilingRun %s evidence binding: %w", key, err)
	}

	return EvidencePassportSnapshot{
		ReadAt: q.now().UTC(), Run: *run.DeepCopy(), Execution: execution, Measurement: measurement,
	}, nil
}

func evidenceIdentity(reference *edgev1alpha1.ProfilingRunEvidenceReference) (evidencestore.ObjectIdentity, error) {
	if reference == nil {
		return evidencestore.ObjectIdentity{}, errors.New("verified evidence is not available")
	}
	identity := evidencestore.ObjectIdentity{
		Store: reference.Store, Key: reference.Key, Digest: reference.Digest, Size: reference.Size,
	}
	if err := identity.Validate(); err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	if reference.SchemaVersion != profilerevidence.ExecutionEvidenceSchemaVersion {
		return evidencestore.ObjectIdentity{}, fmt.Errorf("unsupported evidence schemaVersion %q", reference.SchemaVersion)
	}
	return identity, nil
}

func validatePassportBinding(
	run *edgev1alpha1.ProfilingRun,
	execution profilerevidence.ExecutionEvidence,
) error {
	if run.Status.IntentDigest == "" || execution.IntentDigest != run.Status.IntentDigest {
		return errors.New("evidence intent does not match the frozen Run intent")
	}
	if run.Status.Job == nil ||
		execution.Terminal.Job.Namespace != run.Namespace ||
		execution.Terminal.Job.Name != run.Status.Job.Name ||
		execution.Terminal.Job.UID != run.Status.Job.UID {
		return errors.New("evidence Job does not match the recorded Run Job")
	}
	if run.Status.ActualExecution == nil ||
		execution.Terminal.Pod.Namespace != run.Namespace ||
		execution.Terminal.Pod.Name != run.Status.ActualExecution.Pod.Name ||
		execution.Terminal.Pod.UID != run.Status.ActualExecution.Pod.UID ||
		execution.Terminal.Node.Name != run.Status.ActualExecution.Node.Name ||
		execution.Terminal.Node.UID != run.Status.ActualExecution.Node.UID {
		return errors.New("evidence Pod or Node does not match the recorded Run execution")
	}
	return nil
}
