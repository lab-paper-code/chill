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
	Actuation            *ActuationFacts
	FinalizedAt          time.Time
}

type FinalizeResult struct {
	Reference     evidencestore.ObjectIdentity
	SchemaVersion string
	Reused        bool
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

	rawBytes, err := readBounded(ctx, f.store, request.RawReceipt.Object.Location(), f.maxObjectBytes)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("read raw measurement: %w", err)
	}
	if int64(len(rawBytes)) != request.RawReceipt.Object.Size {
		return FinalizeResult{}, fmt.Errorf("%w: raw measurement size does not match its receipt", evidencestore.ErrContentMismatch)
	}
	raw, err := decodeRawRecord(rawBytes, request.Intent)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := raw.validate(request.Intent); err != nil {
		return FinalizeResult{}, fmt.Errorf("validate raw measurement: %w", err)
	}
	verifiedCandidate, err := profilingrun.VerifyCandidateReport(request.CandidateReportBytes, request.Intent)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("validate candidate report: %w", err)
	}
	candidate := ExecutionEvidence{
		SchemaVersion:   ExecutionEvidenceSchemaVersion,
		Intent:          request.Intent,
		CandidateReport: verifiedCandidate.Record(),
		RawMeasurement:  request.RawReceipt.Object,
		Terminal:        request.Terminal,
		Actuation:       cloneActuationFacts(request.Actuation),
		FinalizedAt:     request.FinalizedAt,
	}
	if err := raw.validateExecution(candidate); err != nil {
		return FinalizeResult{}, fmt.Errorf("validate execution evidence: %w", err)
	}
	candidateBytes, err := candidate.canonicalJSON()
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("canonicalize execution evidence: %w", err)
	}

	existingBytes, err := readBounded(ctx, f.store, request.FinalLocation, f.maxObjectBytes)
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
	retained, err := readBounded(ctx, f.store, request.FinalLocation, f.maxObjectBytes)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("read finalized evidence after create: %w", err)
	}
	if err := validateStoredExecution(retained, identity, raw); err != nil {
		return FinalizeResult{}, fmt.Errorf("verify finalized evidence after create: %w", err)
	}
	return FinalizeResult{
		Reference: identity, SchemaVersion: candidate.SchemaVersion,
		Reused: receipt.Outcome == evidencestore.CreateOutcomeExisting,
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
	if err := raw.validateExecution(existing); err != nil {
		return FinalizeResult{}, fmt.Errorf("validate existing execution evidence: %w", err)
	}
	if existing.RawMeasurement != request.RawReceipt.Object ||
		existing.Terminal.Job.UID != request.Terminal.Job.UID ||
		existing.Terminal.Pod.UID != request.Terminal.Pod.UID ||
		existing.Terminal.Node.UID != request.Terminal.Node.UID {
		return FinalizeResult{}, errors.New("existing execution evidence does not belong to this Run")
	}
	return FinalizeResult{Reference: identity, SchemaVersion: existing.SchemaVersion, Reused: true}, nil
}

func cloneActuationFacts(input *ActuationFacts) *ActuationFacts {
	if input == nil {
		return nil
	}
	copy := *input
	copy.Previous.Actuator = cloneActuatorTarget(copy.Previous.Actuator)
	copy.Target.Actuator = cloneActuatorTarget(copy.Target.Actuator)
	return &copy
}

func cloneActuatorTarget(input profilingrun.ActuatorTarget) profilingrun.ActuatorTarget {
	copy := input
	if input.JetsonNVPModel != nil {
		target := *input.JetsonNVPModel
		copy.JetsonNVPModel = &target
	}
	if input.NvidiaLockedGPUClock != nil {
		target := *input.NvidiaLockedGPUClock
		if target.Mode != nil {
			mode := *target.Mode
			target.Mode = &mode
		}
		copy.NvidiaLockedGPUClock = &target
	}
	return copy
}
