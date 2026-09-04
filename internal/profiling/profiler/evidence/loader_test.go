package evidence

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type loaderStore struct {
	objects map[evidencestore.ObjectLocation][]byte
}

func (s loaderStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	payload, ok := s.objects[location]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func TestLoaderLoadsFinalAndRawObjects(t *testing.T) {
	t.Parallel()
	intent := loadIntent(t)
	raw := validRaw(t, intent)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	rawLocation := evidencestore.ObjectLocation{Store: "evidence", Key: "raw.json"}
	rawIdentity := identityFor(rawLocation, rawBytes)
	execution := validExecutionEvidence(t, intent, raw)
	execution.RawMeasurement = rawIdentity
	executionBytes, err := execution.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	executionLocation := evidencestore.ObjectLocation{Store: "evidence", Key: "execution.json"}
	executionIdentity := identityFor(executionLocation, executionBytes)
	store := loaderStore{objects: map[evidencestore.ObjectLocation][]byte{
		rawLocation: rawBytes, executionLocation: executionBytes,
	}}
	loader, err := NewLoader(store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := loader.Load(context.Background(), executionIdentity)
	if err != nil {
		t.Fatal(err)
	}
	loadedExecution, loadedRaw, err := verified.Records()
	if err != nil {
		t.Fatal(err)
	}
	if loadedExecution.RunConfig.State.Model != execution.RunConfig.State.Model || loadedRaw.PodUID != raw.PodUID {
		t.Fatalf("loaded evidence identity mismatch: %#v %#v", loadedExecution, loadedRaw)
	}

}
