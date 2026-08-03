package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

type ObjectStore interface {
	evidencestore.LocationReader
	evidencestore.ImmutableWriter
}

type FinalizeRequest struct {
	Intent               profilingrun.Intent
	CandidateReportBytes []byte
	RawReceipt           evidencestore.Receipt
	FinalLocation        evidencestore.ObjectLocation
	Terminal             TerminalFacts
	FinalizedAt          time.Time
}

type FinalizeResult struct {
	Reference evidencestore.ObjectIdentity
	Reused    bool
}

// Finalizer coordinates durable evidence handoff. It performs no measurement
// and holds no authoritative state between calls.
type Finalizer struct {
	store          ObjectStore
	maxObjectBytes int64
}

func NewFinalizer(store ObjectStore, maxObjectBytes int64) (*Finalizer, error) {
	if store == nil {
		return nil, errors.New("evidence object store is required")
	}
	if maxObjectBytes < 1 {
		return nil, errors.New("maximum evidence object size must be positive")
	}
	return &Finalizer{store: store, maxObjectBytes: maxObjectBytes}, nil
}

func (f *Finalizer) Finalize(ctx context.Context, request FinalizeRequest) (FinalizeResult, error) {
	if err := request.RawReceipt.Validate(); err != nil {
		return FinalizeResult{}, fmt.Errorf("validate raw receipt: %w", err)
	}
	if err := request.FinalLocation.Validate(); err != nil {
		return FinalizeResult{}, fmt.Errorf("validate final evidence location: %w", err)
	}
	if request.RawReceipt.Object.Location() == request.FinalLocation {
		return FinalizeResult{}, errors.New("raw and finalized evidence locations must differ")
	}

	rawBytes, err := f.read(ctx, request.RawReceipt.Object.Location())
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("read raw measurement: %w", err)
	}
	if err := evidencestore.Verify(bytes.NewReader(rawBytes), request.RawReceipt.Object); err != nil {
		return FinalizeResult{}, fmt.Errorf("verify raw measurement bytes: %w", err)
	}
	raw, err := decodeRawRecord(rawBytes, request.Intent)
	if err != nil {
		return FinalizeResult{}, err
	}
	canonicalRaw, err := raw.canonicalRawJSON(request.Intent)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("validate raw measurement: %w", err)
	}
	if !bytes.Equal(rawBytes, canonicalRaw) {
		return FinalizeResult{}, errors.New("stored raw measurement is not canonical")
	}

	candidate := ExecutionEvidence{
		SchemaVersion:        ExecutionEvidenceSchemaVersion,
		IntentDigest:         raw.rawIntentDigest(),
		Intent:               request.Intent,
		CandidateReportBytes: append([]byte(nil), request.CandidateReportBytes...),
		RawMeasurement:       request.RawReceipt.Object,
		Terminal:             request.Terminal,
		FinalizedAt:          request.FinalizedAt,
	}
	candidateBytes, err := raw.canonicalExecutionJSON(candidate)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("construct execution evidence: %w", err)
	}

	existingBytes, err := f.read(ctx, request.FinalLocation)
	if err == nil {
		return f.reuseExisting(existingBytes, request, raw)
	}
	if !errors.Is(err, evidencestore.ErrNotFound) {
		return FinalizeResult{}, fmt.Errorf("inspect finalized evidence: %w", err)
	}

	identity := identityFor(request.FinalLocation, candidateBytes)
	receipt, err := f.store.PutImmutable(ctx, identity, bytes.NewReader(candidateBytes))
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("create finalized evidence: %w", err)
	}
	if err := receipt.Validate(); err != nil || receipt.Object != identity {
		return FinalizeResult{}, errors.New("finalized evidence writer returned an invalid receipt")
	}
	retained, err := f.read(ctx, request.FinalLocation)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("read finalized evidence after create: %w", err)
	}
	if err := validateStoredExecution(retained, identity, raw); err != nil {
		return FinalizeResult{}, fmt.Errorf("verify finalized evidence after create: %w", err)
	}
	return FinalizeResult{
		Reference: identity,
		Reused:    receipt.Outcome == evidencestore.CreateOutcomeExistingExactMatch,
	}, nil
}

func (f *Finalizer) reuseExisting(
	payload []byte,
	request FinalizeRequest,
	raw rawRecord,
) (FinalizeResult, error) {
	identity := identityFor(request.FinalLocation, payload)
	var existing ExecutionEvidence
	if err := decodeExact(payload, &existing); err != nil {
		return FinalizeResult{}, fmt.Errorf("decode existing execution evidence: %w", err)
	}
	canonical, err := raw.canonicalExecutionJSON(existing)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("validate existing execution evidence: %w", err)
	}
	if !bytes.Equal(payload, canonical) {
		return FinalizeResult{}, errors.New("existing execution evidence is not canonical")
	}
	if existing.RawMeasurement != request.RawReceipt.Object ||
		existing.IntentDigest != raw.rawIntentDigest() ||
		existing.Terminal.Job.UID != request.Terminal.Job.UID ||
		existing.Terminal.Pod.UID != request.Terminal.Pod.UID ||
		existing.Terminal.Node.UID != request.Terminal.Node.UID {
		return FinalizeResult{}, errors.New("existing execution evidence does not belong to this Run")
	}
	return FinalizeResult{Reference: identity, Reused: true}, nil
}
