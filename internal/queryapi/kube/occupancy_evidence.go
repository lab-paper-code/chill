package kube

import (
	"context"
	"errors"
	"io"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/queryapi"
)

// ReadMeasurementEvidence reads retained evidence within the measurement read bound.
func ReadMeasurementEvidence(
	ctx context.Context,
	objects evidencestore.LocationReader,
	maxEvidenceBytes int64,
	location evidencestore.ObjectLocation,
) ([]byte, error) {
	stream, err := objects.OpenLocation(ctx, location)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stream, maxEvidenceBytes+1))
	closeErr := stream.Close()
	if errors.Join(readErr, closeErr) != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if int64(len(raw)) > maxEvidenceBytes {
		return nil, errors.New("measurement evidence exceeds read bound")
	}
	return raw, nil
}

// ProjectDispatchOccupancy reads and interprets evidence without changing its retained values.
func ProjectDispatchOccupancy(
	ctx context.Context,
	objects evidencestore.LocationReader,
	maxEvidenceBytes int64,
	cr api.DispatchOccupancyMeasurement,
	decoder queryapi.OccupancyEvidenceDecoder,
) (queryapi.OccupancyEvidenceProjection, error) {
	var decoded queryapi.DecodedOccupancyEvidence
	var decodeErr, readErr error
	if ref := cr.Status.Result; ref != nil && ref.Key != "" {
		var raw []byte
		raw, readErr = ReadMeasurementEvidence(
			ctx,
			objects,
			maxEvidenceBytes,
			evidencestore.ObjectLocation{Store: ref.Store, Key: ref.Key},
		)
		if readErr == nil {
			decoded, decodeErr = decoder(cr, raw)
		}
	}
	return queryapi.InterpretDispatchOccupancy(cr, decoded, decodeErr, readErr)
}
