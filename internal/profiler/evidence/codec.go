package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func (f *Finalizer) read(ctx context.Context, location evidencestore.ObjectLocation) ([]byte, error) {
	stream, err := f.store.OpenLocation(ctx, location)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, f.maxObjectBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > f.maxObjectBytes {
		return nil, fmt.Errorf("evidence object exceeds %d-byte limit", f.maxObjectBytes)
	}
	return payload, nil
}

func validateStoredExecution(
	payload []byte,
	identity evidencestore.ObjectIdentity,
	raw RawMeasurement,
) error {
	if err := evidencestore.Verify(bytes.NewReader(payload), identity); err != nil {
		return err
	}
	var retained ExecutionEvidence
	if err := decodeExact(payload, &retained); err != nil {
		return err
	}
	canonical, err := retained.CanonicalJSON(raw)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, canonical) {
		return errors.New("retained execution evidence is not canonical")
	}
	return nil
}

func decodeExact(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("evidence contains a trailing JSON value")
	}
	return nil
}

func identityFor(location evidencestore.ObjectLocation, payload []byte) evidencestore.ObjectIdentity {
	sum := sha256.Sum256(payload)
	return evidencestore.ObjectIdentity{
		Store:  location.Store,
		Key:    location.Key,
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
		Size:   int64(len(payload)),
	}
}
