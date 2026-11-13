package evidence

import (
	"context"
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// Loader retrieves and decodes one finalized execution record and the immutable
// raw measurement it references. Validation belongs to finalization.
type Loader struct {
	store          evidencestore.LocationReader
	maxObjectBytes int64
}

func NewLoader(store evidencestore.LocationReader, maxObjectBytes int64) (*Loader, error) {
	if store == nil {
		return nil, errors.New("evidence object reader is required")
	}
	if maxObjectBytes < 1 {
		return nil, errors.New("maximum evidence object size must be positive")
	}
	return &Loader{store: store, maxObjectBytes: maxObjectBytes}, nil
}

func (l *Loader) Load(
	ctx context.Context,
	reference evidencestore.ObjectIdentity,
) (VerifiedEvidence, error) {
	execution, record, err := l.load(ctx, reference)
	if err != nil {
		return VerifiedEvidence{}, err
	}
	raw, ok := record.(RawMeasurement)
	if !ok {
		return VerifiedEvidence{}, errors.New("retained evidence contains idle raw measurement")
	}
	return newVerifiedEvidence(execution, raw), nil
}

// LoadIdle retrieves the distinct model-loaded idle raw schema while reusing
// the same immutable-object decoding path as Load.
func (l *Loader) LoadIdle(
	ctx context.Context,
	reference evidencestore.ObjectIdentity,
) (VerifiedIdleEvidence, error) {
	execution, record, err := l.load(ctx, reference)
	if err != nil {
		return VerifiedIdleEvidence{}, err
	}
	raw, ok := record.(IdleRawMeasurement)
	if !ok {
		return VerifiedIdleEvidence{}, errors.New("retained evidence contains active raw measurement")
	}
	return newVerifiedIdleEvidence(execution, raw), nil
}

func (l *Loader) load(
	ctx context.Context,
	reference evidencestore.ObjectIdentity,
) (ExecutionEvidence, rawRecord, error) {
	finalBytes, err := readBounded(ctx, l.store, reference.Location(), l.maxObjectBytes)
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("read finalized evidence: %w", err)
	}
	var execution ExecutionEvidence
	if err := decodeExact(finalBytes, &execution); err != nil {
		return ExecutionEvidence{}, nil, permanentValidationf("decode finalized evidence: %w", err)
	}
	return l.loadRaw(ctx, execution)
}

func (l *Loader) loadRaw(ctx context.Context, execution ExecutionEvidence) (ExecutionEvidence, rawRecord, error) {
	rawBytes, err := readBounded(ctx, l.store, execution.RawMeasurement.Location(), l.maxObjectBytes)
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("read raw measurement: %w", err)
	}
	raw, err := decodeRawRecord(rawBytes)
	if err != nil {
		return ExecutionEvidence{}, nil, permanentValidationError(err)
	}
	// The raw record carries the config used by the measurement process.
	// The retained envelope's copy remains stored provenance, not a second authority.
	execution.RunConfig = raw.runConfig()
	return execution, raw, nil
}
