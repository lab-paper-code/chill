// Package controller prepares durable inputs for power-mode transition-cost measurements.
package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
)

// ResolvedIdleBaseline is a target-idle aggregate that crossed the retained
// ModelLoadedIdleBaseline result boundary once.
type ResolvedIdleBaseline struct {
	meanWallWatts float64
	reference     idlebaseline.StoredObjectReference
}

func (b ResolvedIdleBaseline) MeanWallWatts() float64 { return b.meanWallWatts }

func (b ResolvedIdleBaseline) Reference() idlebaseline.StoredObjectReference {
	return b.reference
}

// IdleBaselineSource reads one exact retained ModelLoadedIdleBaseline result.
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
	expected idlebaseline.MeasurementState,
	reference idlebaseline.StoredObjectReference,
) (ResolvedIdleBaseline, error) {
	identity := evidencestore.ObjectIdentity{
		Store: reference.Store, Key: reference.Key, Size: reference.Size,
	}
	if err := identity.Validate(); err != nil {
		return ResolvedIdleBaseline{}, fmt.Errorf("validate idle baseline result reference: %w", err)
	}
	if reference.SchemaVersion != idlebaseline.ResultSchemaVersion {
		return ResolvedIdleBaseline{}, fmt.Errorf(
			"idle baseline result schema %q is not current", reference.SchemaVersion,
		)
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
	result, err := idlebaseline.DecodeResult(payload)
	if err != nil {
		return ResolvedIdleBaseline{}, fmt.Errorf("decode idle baseline result: %w", err)
	}
	if result.MeasurementState() != expected {
		return ResolvedIdleBaseline{}, errors.New(
			"ModelLoadedIdleBaseline result measurement state differs from the transition target",
		)
	}
	return ResolvedIdleBaseline{
		meanWallWatts: result.IdleBaseline().ModelLoadedIdleMeanWallWatts,
		reference:     reference,
	}, nil
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
