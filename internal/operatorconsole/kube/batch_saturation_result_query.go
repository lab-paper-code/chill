package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"github.com/lab-paper-code/chill/internal/profiling/campaign"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResultObjectReader mirrors the Evidence Store's exact-identity reader. Its
// implementation is supplied at the composition root, as
// VerifiedEvidenceLoader is.
type ResultObjectReader interface {
	Open(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error)
}

// KubernetesBatchSaturationResultQuery exact-reads a BatchSaturation and
// follows only its status result reference. The record decoder owns schema
// acceptance, so this query bounds the read and projects the sealed result
// without interpreting the measurement method that produced it.
type KubernetesBatchSaturationResultQuery struct {
	reader  client.Reader
	objects ResultObjectReader
	now     func() time.Time
}

func NewKubernetesBatchSaturationResultQuery(
	reader client.Reader,
	objects ResultObjectReader,
) (*KubernetesBatchSaturationResultQuery, error) {
	if reader == nil || objects == nil {
		return nil, errors.New("Kubernetes BatchSaturation reader and result object reader are required")
	}
	return &KubernetesBatchSaturationResultQuery{reader: reader, objects: objects, now: time.Now}, nil
}

func (q *KubernetesBatchSaturationResultQuery) Read(
	ctx context.Context,
	namespace, name string,
) (operatorconsole.BatchSaturationResultSnapshot, error) {
	if err := validateObjectReference("BatchSaturation", namespace, name); err != nil {
		return operatorconsole.BatchSaturationResultSnapshot{}, err
	}

	campaign := &edgev1alpha1.BatchSaturation{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := q.reader.Get(ctx, key, campaign); err != nil {
		return operatorconsole.BatchSaturationResultSnapshot{}, fmt.Errorf("get BatchSaturation %s: %w", key, err)
	}
	identity, err := terminalResultIdentity(campaign.Status.Result)
	if err != nil {
		return operatorconsole.BatchSaturationResultSnapshot{}, fmt.Errorf("read BatchSaturation %s result reference: %w", key, err)
	}
	payload, err := q.readResult(ctx, identity)
	if err != nil {
		return operatorconsole.BatchSaturationResultSnapshot{}, fmt.Errorf("read BatchSaturation %s result: %w", key, err)
	}
	record, err := batchsaturation.DecodeResultRecord(payload)
	if err != nil {
		return operatorconsole.BatchSaturationResultSnapshot{}, fmt.Errorf("decode BatchSaturation %s result: %w", key, err)
	}
	return operatorconsole.BatchSaturationResultSnapshot{
		ReadAt: q.now().UTC(), Campaign: *campaign.DeepCopy(), Result: record,
	}, nil
}

// readResult reads exactly the bytes the reference declares. The store
// interface leaves bounded reading to its caller, and the declared size is the
// bound this caller already holds.
func (q *KubernetesBatchSaturationResultQuery) readResult(
	ctx context.Context,
	identity evidencestore.ObjectIdentity,
) ([]byte, error) {
	stream, err := q.objects.Open(ctx, identity)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, identity.Size+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) != identity.Size {
		return nil, fmt.Errorf("result is %d bytes where the reference declares %d", len(payload), identity.Size)
	}
	return payload, nil
}

func terminalResultIdentity(
	reference *edgev1alpha1.ProfilingRunEvidenceReference,
) (evidencestore.ObjectIdentity, error) {
	if reference == nil {
		return evidencestore.ObjectIdentity{}, operatorconsole.ErrBatchSaturationResultUnavailable
	}
	identity := evidencestore.ObjectIdentity{
		Store: reference.Store, Key: reference.Key, Size: reference.Size,
	}
	if err := identity.Validate(); err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	return identity, nil
}
