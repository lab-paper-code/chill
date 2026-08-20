// Package controller prepares durable inputs for power-mode transition-cost measurements.
package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchsaturation "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// ResolvedIdleBaseline is a target-idle aggregate that crossed the retained
// BatchSaturation result boundary once.
type ResolvedIdleBaseline struct {
	meanWallWatts float64
	reference     batchsaturation.StoredObjectReference
}

func (b ResolvedIdleBaseline) MeanWallWatts() float64 { return b.meanWallWatts }

func (b ResolvedIdleBaseline) Reference() batchsaturation.StoredObjectReference {
	return b.reference
}

// IdleBaselineSource reads one exact retained BatchSaturation result.
type IdleBaselineSource struct {
	reader         evidencestore.Reader
	maxObjectBytes int64
}

func NewIdleBaselineSource(
	reader evidencestore.Reader,
	maxObjectBytes int64,
) (*IdleBaselineSource, error) {
	if reader == nil {
		return nil, errors.New("idle baseline result reader is required")
	}
	if maxObjectBytes < 1 || maxObjectBytes == math.MaxInt64 {
		return nil, errors.New("maximum idle baseline result bytes must be positive and below MaxInt64")
	}
	return &IdleBaselineSource{reader: reader, maxObjectBytes: maxObjectBytes}, nil
}

func (s *IdleBaselineSource) Read(
	ctx context.Context,
	expected batchsaturation.ResultMeasurementState,
	reference batchsaturation.StoredObjectReference,
) (ResolvedIdleBaseline, error) {
	identity := evidencestore.ObjectIdentity{
		Store: reference.Store, Key: reference.Key, Size: reference.Size,
	}
	if err := identity.Validate(); err != nil {
		return ResolvedIdleBaseline{}, fmt.Errorf("validate idle baseline result reference: %w", err)
	}
	if strings.TrimSpace(reference.SchemaVersion) == "" ||
		strings.TrimSpace(reference.SchemaVersion) != reference.SchemaVersion {
		return ResolvedIdleBaseline{}, errors.New("idle baseline result schema version is required without surrounding whitespace")
	}
	if identity.Size > s.maxObjectBytes {
		return ResolvedIdleBaseline{}, fmt.Errorf(
			"idle baseline result declares %d bytes above %d-byte limit", identity.Size, s.maxObjectBytes,
		)
	}
	payload, err := s.read(ctx, identity)
	if err != nil {
		return ResolvedIdleBaseline{}, err
	}
	record, err := batchsaturation.DecodeResultRecord(payload)
	if err != nil {
		return ResolvedIdleBaseline{}, fmt.Errorf("decode idle baseline result: %w", err)
	}
	if record.SchemaVersion() != reference.SchemaVersion {
		return ResolvedIdleBaseline{}, errors.New("idle baseline result schema differs from its reference")
	}
	spec := record.Spec()
	if !successfulBatchSaturationOutcome(spec.Outcome) {
		return ResolvedIdleBaseline{}, fmt.Errorf("BatchSaturation result outcome %q is not successful", spec.Outcome)
	}
	if spec.MeasurementState != expected {
		return ResolvedIdleBaseline{}, errors.New("BatchSaturation result measurement state differs from the transition target")
	}
	detail, err := profilederivation.DecodeSequentialResult(spec.Detail)
	if err != nil {
		return ResolvedIdleBaseline{}, fmt.Errorf("decode BatchSaturation sequential result: %w", err)
	}
	if len(detail.IdleBaselinesByState) != 1 {
		return ResolvedIdleBaseline{}, fmt.Errorf(
			"BatchSaturation result contains %d idle baselines, want exactly one",
			len(detail.IdleBaselinesByState),
		)
	}
	var meanWallWatts float64
	for _, baseline := range detail.IdleBaselinesByState {
		meanWallWatts = baseline.ModelLoadedIdleMeanWallWatts
	}
	return ResolvedIdleBaseline{meanWallWatts: meanWallWatts, reference: reference}, nil
}

func (s *IdleBaselineSource) read(
	ctx context.Context,
	identity evidencestore.ObjectIdentity,
) ([]byte, error) {
	stream, err := s.reader.Open(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("open idle baseline result: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, identity.Size+1))
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if int64(len(payload)) != identity.Size {
		return nil, fmt.Errorf(
			"idle baseline result is %d bytes where the reference declares %d",
			len(payload), identity.Size,
		)
	}
	return payload, nil
}

func successfulBatchSaturationOutcome(outcome batchsaturation.TerminalOutcome) bool {
	switch outcome {
	case "Stopped/NoFurtherImprovement", "Stopped/EnergyDivergence", "Stopped/CapabilityBoundary":
		return true
	default:
		return false
	}
}
