package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VerifiedEvidenceView exposes records only after the production loader has
// verified both immutable objects and their recorded ProfilingRun binding.
type VerifiedEvidenceView interface {
	Records() (profilerevidence.ExecutionEvidence, profilerevidence.RawMeasurement, error)
}

// VerifiedEvidenceLoader retrieves the boundary-sealed view named by an
// immutable object identity.
type VerifiedEvidenceLoader interface {
	Load(
		context.Context,
		*edgev1alpha1.ProfilingRun,
		evidencestore.ObjectIdentity,
	) (VerifiedEvidenceView, error)
}

// KubernetesEvidencePassportQuery exact-reads a ProfilingRun and follows only
// its status evidence reference. The loader owns all evidence and Run-binding
// verification; this query only projects its sealed result.
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
) (operatorconsole.EvidencePassportSnapshot, error) {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(namespace) != namespace ||
		strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return operatorconsole.EvidencePassportSnapshot{}, errors.New("ProfilingRun namespace and name are required without surrounding whitespace")
	}

	run := &edgev1alpha1.ProfilingRun{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := q.reader.Get(ctx, key, run); err != nil {
		return operatorconsole.EvidencePassportSnapshot{}, fmt.Errorf("get ProfilingRun %s: %w", key, err)
	}
	reference, err := evidenceIdentity(run.Status.Evidence)
	if err != nil {
		return operatorconsole.EvidencePassportSnapshot{}, fmt.Errorf("read ProfilingRun %s evidence reference: %w", key, err)
	}
	verified, err := q.loader.Load(ctx, run, reference)
	if err != nil {
		return operatorconsole.EvidencePassportSnapshot{}, fmt.Errorf("load ProfilingRun %s evidence: %w", key, err)
	}
	execution, measurement, err := verified.Records()
	if err != nil {
		return operatorconsole.EvidencePassportSnapshot{}, fmt.Errorf("project ProfilingRun %s verified evidence: %w", key, err)
	}

	return operatorconsole.EvidencePassportSnapshot{
		ReadAt: q.now().UTC(), Run: *run.DeepCopy(), Execution: execution, Measurement: measurement,
	}, nil
}

func evidenceIdentity(reference *edgev1alpha1.ProfilingRunEvidenceReference) (evidencestore.ObjectIdentity, error) {
	if reference == nil {
		return evidencestore.ObjectIdentity{}, errors.New("verified evidence is not available")
	}
	identity := evidencestore.ObjectIdentity{
		Store: reference.Store, Key: reference.Key, Size: reference.Size,
	}
	if err := identity.Validate(); err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	if !profilerevidence.IsSupportedExecutionEvidenceSchemaVersion(reference.SchemaVersion) {
		return evidencestore.ObjectIdentity{}, fmt.Errorf("unsupported evidence schemaVersion %q", reference.SchemaVersion)
	}
	return identity, nil
}
