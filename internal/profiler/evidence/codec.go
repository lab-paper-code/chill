package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func validateStoredExecution(
	payload []byte,
	identity evidencestore.ObjectIdentity,
	raw rawRecord,
	intentDigest string,
) error {
	if int64(len(payload)) != identity.Size {
		return fmt.Errorf("retained execution evidence size %d does not match declared size %d", len(payload), identity.Size)
	}
	var retained ExecutionEvidence
	if err := decodeExact(payload, &retained); err != nil {
		return err
	}
	if err := raw.validateExecution(retained, intentDigest); err != nil {
		return err
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
