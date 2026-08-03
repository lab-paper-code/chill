package evidence

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func readVerified(
	ctx context.Context,
	reader evidencestore.LocationReader,
	identity evidencestore.ObjectIdentity,
	maximumBytes int64,
) ([]byte, error) {
	payload, err := readBounded(ctx, reader, identity.Location(), maximumBytes)
	if err != nil {
		return nil, err
	}
	if err := evidencestore.Verify(bytes.NewReader(payload), identity); err != nil {
		return nil, err
	}
	return payload, nil
}

func readBounded(
	ctx context.Context,
	reader evidencestore.LocationReader,
	location evidencestore.ObjectLocation,
	maximumBytes int64,
) ([]byte, error) {
	stream, err := reader.OpenLocation(ctx, location)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, maximumBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > maximumBytes {
		return nil, fmt.Errorf("evidence object exceeds %d-byte limit", maximumBytes)
	}
	return payload, nil
}
