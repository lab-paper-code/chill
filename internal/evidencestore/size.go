package evidencestore

import (
	"errors"
	"fmt"
	"io"
)

// ValidateSize consumes content and requires exactly the declared number of
// bytes. It does not close content; ownership remains with the caller.
func ValidateSize(content io.Reader, expectedSize int64) error {
	if content == nil {
		return fmt.Errorf("%w: content reader is nil", ErrContentMismatch)
	}
	if expectedSize < 1 {
		return fmt.Errorf("%w: expected size must be positive", ErrInvalidIdentity)
	}

	written, err := io.CopyN(io.Discard, content, expectedSize)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: expected %d bytes, read %d", ErrContentMismatch, expectedSize, written)
		}
		return fmt.Errorf("read evidence content: %w", err)
	}

	var extra [1]byte
	if _, err := io.ReadFull(content, extra[:]); err == nil {
		return fmt.Errorf("%w: content exceeds expected size %d", ErrContentMismatch, expectedSize)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("read evidence content boundary: %w", err)
	}
	return nil
}
