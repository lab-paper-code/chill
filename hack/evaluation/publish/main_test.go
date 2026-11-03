package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

type publicationMemoryStore struct {
	keys     []string
	stored   map[string]bool
	fail     string
	existing map[string]evidencestore.ObjectIdentity
}

func (s *publicationMemoryStore) PutImmutable(_ context.Context, object evidencestore.ObjectIdentity, body io.Reader) (evidencestore.Receipt, error) {
	s.keys = append(s.keys, object.Key)
	if object.Key == s.fail {
		return evidencestore.Receipt{}, evidencestore.ErrTransport
	}
	if s.stored == nil {
		s.stored = map[string]bool{}
	}
	if s.stored[object.Key] {
		return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: object, Outcome: evidencestore.CreateOutcomeExisting}, nil
	}
	if err := object.Validate(); err != nil {
		return evidencestore.Receipt{}, err
	}
	size, err := io.Copy(io.Discard, body)
	if err != nil || size != object.Size {
		return evidencestore.Receipt{}, evidencestore.ErrContentMismatch
	}
	s.stored[object.Key] = true
	return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: object, Outcome: evidencestore.CreateOutcomeCreated}, nil
}

func (s *publicationMemoryStore) Stat(ctx context.Context, location evidencestore.ObjectLocation) (evidencestore.ObjectIdentity, error) {
	if err := ctx.Err(); err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	object, ok := s.existing[location.Key]
	if !ok || object.Store != location.Store {
		return evidencestore.ObjectIdentity{}, evidencestore.ErrNotFound
	}
	return object, nil
}

func publicationFixture(t *testing.T) publicationManifest {
	t.Helper()
	directory := t.TempDir()
	prefix := httpapi.ExperimentPrefix + "run-1/"
	raw := publicationObject{Store: "evidence", Key: prefix + "raw/phase.json", LocalPath: filepath.Join(directory, "phase.json")}
	if err := os.WriteFile(raw.LocalPath, []byte(`{"startUTC":"2026-10-24T01:00:00Z","endUTC":"2026-10-24T01:01:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	summary := httpapi.ExperimentSummary{
		SchemaVersion: httpapi.ExperimentSchemaVersion, ID: "run-1", From: from, To: from.Add(time.Minute), Status: "NOT_READY",
		Stages:  []httpapi.ExperimentStage{{ID: "E5-2", From: from, To: from.Add(time.Minute), KPIs: json.RawMessage(`{"status":"FAIL","visibility":"PRIVATE_OPERATIONAL_ONLY","wholePoolJoules":null,"reasons":["unmeasured"]}`)}},
		Sources: []httpapi.ExperimentSource{{Store: raw.Store, Key: raw.Key}},
	}
	manifest := publicationManifest{Objects: []publicationObject{raw}, Summary: publicationObject{Store: "evidence", Key: prefix + "summary.json", LocalPath: filepath.Join(directory, "summary.json")}}
	writePublicationFixture(t, manifest.Summary.LocalPath, summary)
	return manifest
}

func writePublicationFixture(t *testing.T, name string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPublishRawBeforeSummaryAndRefuseSameID(t *testing.T) {
	manifest := publicationFixture(t)
	store := &publicationMemoryStore{}
	receipt, err := publish(context.Background(), store, store, "evidence", manifest)
	if err != nil || receipt.Outcome != evidencestore.CreateOutcomeCreated || len(store.keys) != 2 ||
		store.keys[0] != manifest.Objects[0].Key || store.keys[1] != manifest.Summary.Key {
		t.Fatalf("publication ordering/receipt: %+v %v keys=%v", receipt, err, store.keys)
	}
	if _, err := publish(context.Background(), store, store, "evidence", manifest); !errors.Is(err, evidencestore.ErrPrecondition) {
		t.Fatalf("existing summary was accepted as new: %v", err)
	}
}

func TestPublishLinksExistingFrozenSourceWithoutReupload(t *testing.T) {
	for _, name := range []string{"verified", "missing", "size mismatch", "other Run", "other store"} {
		t.Run(name, func(t *testing.T) {
			manifest := publicationFixture(t)
			source := evidencestore.ObjectIdentity{Store: "evidence", Key: "experiments/run-1/run/prepare/input-snapshot.json", Size: 17}
			observed := source
			store := &publicationMemoryStore{existing: map[string]evidencestore.ObjectIdentity{source.Key: observed}}
			switch name {
			case "missing":
				delete(store.existing, source.Key)
			case "size mismatch":
				observed.Size++
				store.existing[source.Key] = observed
			case "other Run":
				source.Key = "experiments/other/run/prepare/input-snapshot.json"
				store.existing[source.Key] = source
			case "other store":
				source.Store = "foreign"
			}
			var summary httpapi.ExperimentSummary
			if err := readPublicationJSON(manifest.Summary.LocalPath, &summary); err != nil {
				t.Fatal(err)
			}
			summary.Sources = append(summary.Sources, httpapi.ExperimentSource{Store: source.Store, Key: source.Key})
			writePublicationFixture(t, manifest.Summary.LocalPath, summary)
			manifest.ExistingSources = []evidencestore.ObjectIdentity{source}
			_, err := publish(context.Background(), store, store, "evidence", manifest)
			if name == "verified" {
				if err != nil || len(store.keys) != 2 || store.keys[1] != manifest.Summary.Key {
					t.Fatalf("verified frozen source publication: err=%v writes=%v", err, store.keys)
				}
				for _, key := range store.keys {
					if key == source.Key {
						t.Fatal("frozen source was re-uploaded")
					}
				}
			} else if err == nil || len(store.keys) != 0 {
				t.Fatalf("unverified source must fail before publication: err=%v writes=%v", err, store.keys)
			}
		})
	}
}

func TestPublishPartialFailureRequiresNewID(t *testing.T) {
	manifest := publicationFixture(t)
	second := publicationObject{
		Store:     manifest.Objects[0].Store,
		Key:       httpapi.ExperimentPrefix + "run-1/raw/second.json",
		LocalPath: filepath.Join(t.TempDir(), "second.json"),
	}
	if err := os.WriteFile(second.LocalPath, []byte(`{"trial":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest.Objects = append(manifest.Objects, second)
	store := &publicationMemoryStore{fail: second.Key}
	if _, err := publish(context.Background(), store, store, "evidence", manifest); !errors.Is(err, evidencestore.ErrTransport) {
		t.Fatalf("expected partial publication failure: %v", err)
	}
	if !store.stored[manifest.Objects[0].Key] || store.stored[manifest.Summary.Key] {
		t.Fatal("partial publication must preserve the first raw object but not publish the summary")
	}
	// A new run cannot attach its summary to the first run's immutable evidence.
	if err := os.WriteFile(manifest.Objects[0].LocalPath, []byte(`{"differentRun":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	store.fail = ""
	store.keys = nil
	if _, err := publish(context.Background(), store, store, "evidence", manifest); !errors.Is(err, evidencestore.ErrPrecondition) {
		t.Fatalf("republication must require a new experiment ID: %v", err)
	}
	if len(store.keys) != 1 || store.keys[0] != manifest.Objects[0].Key || store.stored[manifest.Summary.Key] {
		t.Fatalf("collision must stop before new evidence or summary writes: %v", store.keys)
	}
}

func TestPublishNeverCompletesWithoutRawEvidence(t *testing.T) {
	for _, name := range []string{"raw store failure", "raw file missing", "unpublished source", "summary raw key", "other experiment", "other store", "canceled"} {
		t.Run(name, func(t *testing.T) {
			manifest := publicationFixture(t)
			store := &publicationMemoryStore{}
			ctx := context.Background()
			switch name {
			case "raw store failure":
				store.fail = manifest.Objects[0].Key
			case "raw file missing":
				manifest.Objects[0].LocalPath += ".missing"
			case "unpublished source":
				manifest.Objects = []publicationObject{}
			case "summary raw key":
				manifest.Objects[0].Key = manifest.Summary.Key
			case "other experiment":
				manifest.Objects[0].Key = httpapi.ExperimentPrefix + "other/raw.json"
			case "other store":
				manifest.Objects[0].Store = "unconfigured"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := publish(ctx, store, store, "evidence", manifest); err == nil {
				t.Fatal("incomplete publication accepted")
			}
			if store.stored[manifest.Summary.Key] {
				t.Fatal("discoverable summary published without preserved raw evidence")
			}
		})
	}
}

func TestRunUsesExistingS3ImmutableContract(t *testing.T) {
	manifest := publicationFixture(t)
	var keys []string
	var mutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" {
			t.Errorf("not an immutable-create request: method=%s precondition=%q", r.Method, r.Header.Get("If-None-Match"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mutex.Lock()
		keys = append(keys, strings.TrimPrefix(r.URL.Path, "/bucket/"))
		mutex.Unlock()
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		w.Header().Set("ETag", `"test-etag"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	directory := t.TempDir()
	configPath, manifestPath := filepath.Join(directory, "config.json"), filepath.Join(directory, "manifest.json")
	writePublicationFixture(t, configPath, publicationConfig{LogicalStore: "evidence", Bucket: "bucket", Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "test-access", SecretAccessKey: "test-secret"})
	writePublicationFixture(t, manifestPath, manifest)
	var output bytes.Buffer
	if err := run(context.Background(), configPath, manifestPath, &output); err != nil {
		t.Fatal(err)
	}
	var receipt evidencestore.Receipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if receipt.Validate() != nil || receipt.Outcome != evidencestore.CreateOutcomeCreated || receipt.Object.Key != manifest.Summary.Key ||
		len(keys) != 2 || keys[0] != manifest.Objects[0].Key || keys[1] != manifest.Summary.Key {
		t.Fatalf("S3 publication receipt=%+v keys=%v", receipt, keys)
	}
}

func TestPublishOptionalOutcomeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		definition json.RawMessage
		valid      bool
	}{
		{"absent", nil, true},
		{"object", json.RawMessage(`{"question":"compare","future":{"opaque":true},"stages":[{"id":"baseline"}]}`), true},
		{"empty object", json.RawMessage(` { } `), true},
		{"scalar", json.RawMessage(`42`), false},
		{"string", json.RawMessage(`"definition"`), false},
		{"array", json.RawMessage(`[]`), false},
		{"null", json.RawMessage(`null`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := publicationFixture(t)
			var summary httpapi.ExperimentSummary
			if err := readPublicationJSON(manifest.Summary.LocalPath, &summary); err != nil {
				t.Fatal(err)
			}
			// Exercise outcome independently from definition validation.
			summary.Definition = json.RawMessage(`{}`)
			summary.Outcome = tc.definition
			writePublicationFixture(t, manifest.Summary.LocalPath, summary)
			store := &publicationMemoryStore{}
			receipt, err := publish(context.Background(), store, store, "evidence", manifest)
			if tc.valid {
				if err != nil || receipt.Outcome != evidencestore.CreateOutcomeCreated ||
					len(store.keys) != 2 || store.keys[1] != manifest.Summary.Key {
					t.Fatalf("valid definition rejected: receipt=%+v err=%v keys=%v", receipt, err, store.keys)
				}
			} else if err == nil || len(store.keys) != 0 {
				t.Fatalf("invalid definition must fail before uploads: err=%v keys=%v", err, store.keys)
			}
		})
	}
}
