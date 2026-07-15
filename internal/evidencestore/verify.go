package evidencestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Verify streams content once and verifies its exact byte size and SHA-256.
// It does not close content; ownership remains with the caller.
func Verify(content io.Reader, expected ObjectIdentity) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if content == nil {
		return fmt.Errorf("%w: content reader is nil", ErrContentMismatch)
	}

	hash := sha256.New()
	written, err := io.CopyN(hash, content, expected.Size)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: expected %d bytes, read %d", ErrContentMismatch, expected.Size, written)
		}
		return fmt.Errorf("read evidence content: %w", err)
	}

	var extra [1]byte
	if _, err := io.ReadFull(content, extra[:]); err == nil {
		return fmt.Errorf("%w: content exceeds expected size %d", ErrContentMismatch, expected.Size)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("read evidence content boundary: %w", err)
	}

	actualDigest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actualDigest != expected.Digest {
		return fmt.Errorf("%w: digest does not match expected identity", ErrContentMismatch)
	}
	return nil
}
