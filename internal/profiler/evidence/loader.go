package evidence

import (
	"context"
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// Loader independently retrieves and validates one finalized execution record
// and the immutable raw measurement it references. It retains no Run state.
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
// the same immutable-object and finalized-evidence verification path as Load.
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
	finalBytes, err := readExpectedSize(ctx, l.store, reference, l.maxObjectBytes)
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("read finalized evidence: %w", err)
	}
	var execution ExecutionEvidence
	if err := decodeExact(finalBytes, &execution); err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("decode finalized evidence: %w", err)
	}
	rawBytes, err := readExpectedSize(ctx, l.store, execution.RawMeasurement, l.maxObjectBytes)
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("read raw measurement: %w", err)
	}
	raw, err := decodeRawRecord(rawBytes, execution.Intent)
	if err != nil {
		return ExecutionEvidence{}, nil, err
	}
	intentDigest, err := execution.Intent.Digest()
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("validate retained Run intent: %w", err)
	}
	if err := raw.validate(execution.Intent, intentDigest); err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("validate raw measurement: %w", err)
	}
	if err := raw.validateExecution(execution, intentDigest); err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("validate finalized evidence: %w", err)
	}
	return execution, raw, nil
}
