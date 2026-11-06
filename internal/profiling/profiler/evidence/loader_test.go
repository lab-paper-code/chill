package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type loaderStore struct {
	objects map[evidencestore.ObjectLocation][]byte
	openErr error
	readErr error
	closeErr error
}

func (s loaderStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	payload, ok := s.objects[location]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return loaderStream{Reader: bytes.NewReader(payload), readErr: s.readErr, closeErr: s.closeErr}, nil
}

type loaderStream struct {
	io.Reader
	readErr error
	closeErr error
}

func (s loaderStream) Read(p []byte) (int, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	return s.Reader.Read(p)
}

func (s loaderStream) Close() error { return s.closeErr }

func TestBoundedReadPreservesStorageAndStreamFailures(t *testing.T) {
	location := evidencestore.ObjectLocation{Store: "evidence", Key: "opaque"}
	failure := errors.New("storage unavailable")
	for _, store := range []loaderStore{
		{openErr: failure},
		{objects: map[evidencestore.ObjectLocation][]byte{location: []byte("{}")}, readErr: failure},
		{objects: map[evidencestore.ObjectLocation][]byte{location: []byte("{}")}, closeErr: failure},
	} {
		if _, err := readBounded(context.Background(), store, location, 1024); !errors.Is(err, failure) {
			t.Fatalf("storage or stream error was lost: %v", err)
		}
	}
}

func TestLoaderLoadsFinalAndRawObjects(t *testing.T) {
	t.Parallel()
	config := loadRunConfig(t)
	raw := validRaw(t, config)
	rawBytes, err := raw.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	rawLocation := evidencestore.ObjectLocation{Store: "evidence", Key: " opaque raw.json "}
	rawIdentity := identityFor(rawLocation, rawBytes)
	execution := validExecutionEvidence(t, config, raw)
	execution.RawMeasurement = rawIdentity
	execution.RunConfig.Measurement.Batch++
	execution.RunConfig.Candidate.Inputs.ModelSpec.ResourceVersion = "older-envelope"
	executionBytes, err := execution.CanonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	executionLocation := evidencestore.ObjectLocation{Store: "evidence", Key: " opaque execution.json "}
	executionIdentity := identityFor(executionLocation, executionBytes)
	store := loaderStore{objects: map[evidencestore.ObjectLocation][]byte{
		rawLocation: append(rawBytes, '\n'), executionLocation: append(executionBytes, '\n'),
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
	if loadedExecution.RunConfig.Measurement.Batch != raw.RunConfig.Measurement.Batch || loadedRaw.PodUID != raw.PodUID {
		t.Fatalf("loaded evidence lost the measured config authority: %#v %#v", loadedExecution, loadedRaw)
	}
	for _, test := range []struct {
		name string
		location evidencestore.ObjectLocation
		payload []byte
	}{
		{name: "missing final", location: executionLocation},
		{name: "malformed final", location: executionLocation, payload: []byte("{")},
		{name: "oversized final", location: executionLocation, payload: bytes.Repeat([]byte(" "), 1<<20+1)},
		{name: "missing raw", location: rawLocation},
		{name: "malformed raw", location: rawLocation, payload: []byte("{")},
		{name: "oversized raw", location: rawLocation, payload: bytes.Repeat([]byte(" "), 1<<20+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := store.objects[test.location]
			defer func() { store.objects[test.location] = original }()
			if test.payload == nil {
				delete(store.objects, test.location)
			} else {
				store.objects[test.location] = test.payload
			}
			if _, err := loader.Load(context.Background(), executionIdentity); err == nil {
				t.Fatal("unreadable evidence became a verified execution")
			}
		})
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
