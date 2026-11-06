package evidence

import (
	"context"
	"fmt"
	"io"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

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
