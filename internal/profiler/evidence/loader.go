package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

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
) (ExecutionEvidence, RawMeasurement, error) {
	execution, record, err := l.load(ctx, reference)
	if err != nil {
		return ExecutionEvidence{}, RawMeasurement{}, err
	}
	raw, ok := record.(RawMeasurement)
	if !ok {
		return ExecutionEvidence{}, RawMeasurement{}, errors.New("retained evidence contains idle raw measurement")
	}
	return execution, raw, nil
}

// LoadIdle retrieves the distinct model-loaded idle raw schema while reusing
// the same immutable-object and finalized-evidence verification path as Load.
func (l *Loader) LoadIdle(
	ctx context.Context,
	reference evidencestore.ObjectIdentity,
) (ExecutionEvidence, IdleRawMeasurement, error) {
	execution, record, err := l.load(ctx, reference)
	if err != nil {
		return ExecutionEvidence{}, IdleRawMeasurement{}, err
	}
	raw, ok := record.(IdleRawMeasurement)
	if !ok {
		return ExecutionEvidence{}, IdleRawMeasurement{}, errors.New("retained evidence contains active raw measurement")
	}
	return execution, raw, nil
}

func (l *Loader) load(
	ctx context.Context,
	reference evidencestore.ObjectIdentity,
) (ExecutionEvidence, rawRecord, error) {
	finalBytes, err := l.read(ctx, reference)
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("read finalized evidence: %w", err)
	}
	var execution ExecutionEvidence
	if err := decodeExact(finalBytes, &execution); err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("decode finalized evidence: %w", err)
	}
	if err := execution.RawMeasurement.Validate(); err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("validate raw measurement reference: %w", err)
	}
	rawBytes, err := l.read(ctx, execution.RawMeasurement)
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("read raw measurement: %w", err)
	}
	raw, err := decodeRawRecord(rawBytes, execution.Intent)
	if err != nil {
		return ExecutionEvidence{}, nil, err
	}
	canonicalRaw, err := raw.canonicalRawJSON(execution.Intent)
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("validate raw measurement: %w", err)
	}
	if !bytes.Equal(rawBytes, canonicalRaw) {
		return ExecutionEvidence{}, nil, errors.New("retained raw measurement is not canonical")
	}
	canonicalExecution, err := raw.canonicalExecutionJSON(execution)
	if err != nil {
		return ExecutionEvidence{}, nil, fmt.Errorf("validate finalized evidence: %w", err)
	}
	if !bytes.Equal(finalBytes, canonicalExecution) {
		return ExecutionEvidence{}, nil, errors.New("retained finalized evidence is not canonical")
	}
	return execution, raw, nil
}

func (l *Loader) read(ctx context.Context, identity evidencestore.ObjectIdentity) ([]byte, error) {
	stream, err := l.store.OpenLocation(ctx, identity.Location())
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, l.maxObjectBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > l.maxObjectBytes {
		return nil, fmt.Errorf("evidence object exceeds %d-byte limit", l.maxObjectBytes)
	}
	if err := evidencestore.Verify(bytes.NewReader(payload), identity); err != nil {
		return nil, err
	}
	return payload, nil
}
