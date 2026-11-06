package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

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
	return evidencestore.ObjectIdentity{
		Store: location.Store,
		Key:   location.Key,
		Size:  int64(len(payload)),
	}
}
