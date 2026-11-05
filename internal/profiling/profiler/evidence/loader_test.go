package evidence

import (
	"bytes"
	"context"
	"encoding/json"
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
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	rawLocation := evidencestore.ObjectLocation{Store: "evidence", Key: "raw.json"}
	rawIdentity := identityFor(rawLocation, rawBytes)
	execution := validExecutionEvidence(t, config, raw)
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
	// An S-only raw object does not become admissible by retaining the selected
	// unreleased schema label and a valid immutable execution envelope.
	var legacy map[string]any
	if err := json.Unmarshal(rawBytes, &legacy); err != nil {
		t.Fatal(err)
	}
	runtime := legacy["runtime"].(map[string]any)
	for _, repetition := range runtime["repetitionResults"].([]any) {
		for _, row := range repetition.(map[string]any)["nativeSamples"].([]any) {
			sample := row.(map[string]any)
			for _, key := range []string{"serviceDurationNanos", "precedingIdleNanos", "trialID"} {
				delete(sample, key)
			}
		}
	}
	legacyBytes, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[rawLocation] = legacyBytes
	execution.RawMeasurement = identityFor(rawLocation, legacyBytes)
	executionBytes, err = json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[executionLocation] = executionBytes
	if _, err := loader.Load(context.Background(), identityFor(executionLocation, executionBytes)); err == nil {
		t.Fatal("S-only evidence under the selected schema crossed the retained reader")
	}

}
