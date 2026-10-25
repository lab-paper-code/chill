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
	keys   []string
	stored map[string]bool
	fail   string
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
	receipt, err := publish(context.Background(), store, "evidence", manifest)
	if err != nil || receipt.Outcome != evidencestore.CreateOutcomeCreated || len(store.keys) != 2 ||
		store.keys[0] != manifest.Objects[0].Key || store.keys[1] != manifest.Summary.Key {
		t.Fatalf("publication ordering/receipt: %+v %v keys=%v", receipt, err, store.keys)
	}
	if _, err := publish(context.Background(), store, "evidence", manifest); !errors.Is(err, evidencestore.ErrPrecondition) {
		t.Fatalf("existing summary was accepted as new: %v", err)
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
	if _, err := publish(context.Background(), store, "evidence", manifest); !errors.Is(err, evidencestore.ErrTransport) {
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
	if _, err := publish(context.Background(), store, "evidence", manifest); !errors.Is(err, evidencestore.ErrPrecondition) {
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
			if _, err := publish(ctx, store, "evidence", manifest); err == nil {
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
