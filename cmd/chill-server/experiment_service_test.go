package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

type experimentMemoryStore struct {
	objects  []evidencestore.ObjectIdentity
	bodies   map[string]string
	opened   []string
	prefixes []string
	listErr  error
	openErr  error
	closeErr error
	contexts []context.Context
}

func (s *experimentMemoryStore) List(ctx context.Context, prefix string) ([]evidencestore.ObjectIdentity, error) {
	s.contexts = append(s.contexts, ctx)
	s.prefixes = append(s.prefixes, prefix)
	if s.listErr != nil {
		return nil, s.listErr
	}
	var result []evidencestore.ObjectIdentity
	for _, object := range s.objects {
		if strings.HasPrefix(object.Key, prefix) {
			result = append(result, object)
		}
	}
	return result, ctx.Err()
}

func (s *experimentMemoryStore) Open(ctx context.Context, object evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	s.contexts = append(s.contexts, ctx)
	s.opened = append(s.opened, object.Key)
	if s.openErr != nil {
		return nil, s.openErr
	}
	body, ok := s.bodies[object.Key]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return experimentReadCloser{Reader: strings.NewReader(body), closeErr: s.closeErr}, ctx.Err()
}

type experimentReadCloser struct {
	io.Reader
	closeErr error
}

func (r experimentReadCloser) Close() error { return r.closeErr }

func (s *experimentMemoryStore) add(t *testing.T, summary httpapi.ExperimentSummary) string {
	t.Helper()
	body, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	key := httpapi.ExperimentPrefix + summary.ID + "/summary.json"
	s.objects = append(s.objects, evidencestore.ObjectIdentity{Store: "evidence", Key: key, Size: int64(len(body))})
	if s.bodies == nil {
		s.bodies = map[string]string{}
	}
	s.bodies[key] = string(body)
	return key
}

func experimentFixture(id string, from time.Time) httpapi.ExperimentSummary {
	return httpapi.ExperimentSummary{
		SchemaVersion: httpapi.ExperimentSchemaVersion, ID: id, From: from, To: from.Add(time.Minute), Status: "NOT_READY",
		Stages: []httpapi.ExperimentStage{{ID: "full-E5-2", From: from, To: from.Add(time.Minute), KPIs: json.RawMessage(`{"visibility":"PRIVATE_OPERATIONAL_ONLY","status":"FAIL","requests":{"offered":5,"outcomes":{"completed":3,"rejected":2},"success":{"numerator":3,"denominator":5},"slo":{"numerator":2,"denominator":5},"completedLatencyNS":{"p95":17}},"wholePoolJoules":null,"activePoolJoules":null,"reasons":["power missing"],"population":"offered requests in [from,to); pool energy shared across workloads"}`)}},
		Sources: []httpapi.ExperimentSource{{Store: "evidence", Key: "serving/observations/hour/requests.jsonl.gz"}},
	}
}

func TestExperimentSummaryReadPreservesPublishedEvidenceAndSorts(t *testing.T) {
	store := &experimentMemoryStore{}
	from := time.Date(2026, 10, 24, 1, 0, 0, 123, time.UTC)
	store.add(t, experimentFixture("older", from.Add(-time.Hour)))
	store.add(t, experimentFixture("z", from))
	store.add(t, experimentFixture("a", from))
	store.objects = append(store.objects,
		evidencestore.ObjectIdentity{Store: "evidence", Key: httpapi.ExperimentPrefix + "unfinished/phase.json", Size: 1},
		evidencestore.ObjectIdentity{Store: "evidence", Key: httpapi.ExperimentPrefix + "../summary.json", Size: 1},
		evidencestore.ObjectIdentity{Store: "evidence", Key: httpapi.ExperimentPrefix + "nested/run/summary.json", Size: 1},
		evidencestore.ObjectIdentity{Store: "evidence", Key: httpapi.ExperimentPrefix + "a/summary.json/raw", Size: 1},
	)
	service := &experimentService{reader: store, lister: store, store: "evidence", maxBytes: 1 << 20}
	ctx := context.Background()
	list, err := service.list(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 || list.Items[0].ID != "a" || list.Items[1].ID != "z" || list.Items[2].ID != "older" || len(store.opened) != 3 {
		t.Fatalf("summaries not filtered and sorted: %+v opened=%v", list, store.opened)
	}
	detail, err := service.get(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if detail.ID != list.Items[0].ID || detail.Status != "NOT_READY" || !detail.From.Equal(from) ||
		!detail.Stages[0].To.Equal(from.Add(time.Minute)) || detail.Sources[0] != list.Items[0].Sources[0] {
		t.Fatalf("published identity, phase boundary or sources changed: %+v", detail)
	}
	var kpis map[string]any
	if err := json.Unmarshal(detail.Stages[0].KPIs, &kpis); err != nil {
		t.Fatal(err)
	}
	if kpis["wholePoolJoules"] != nil || kpis["activePoolJoules"] != nil || kpis["status"] != "FAIL" || kpis["visibility"] != "PRIVATE_OPERATIONAL_ONLY" || len(kpis["reasons"].([]any)) != 1 {
		t.Fatalf("missing/failed/private interpretations changed: %v", kpis)
	}
	requests := kpis["requests"].(map[string]any)
	if requests["offered"] != float64(5) || requests["slo"].(map[string]any)["numerator"] != float64(2) || requests["slo"].(map[string]any)["denominator"] != float64(5) {
		t.Fatalf("KPI populations changed: %v", requests)
	}
}

func TestExperimentSummaryMissingIsNotStoreFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		list bool
	}{
		{name: "missing"},
		{name: "list transport", err: evidencestore.ErrTransport, list: true},
		{name: "open authentication", err: evidencestore.ErrAuthentication},
		{name: "open missing", err: evidencestore.ErrNotFound},
		{name: "close failed", err: io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &experimentMemoryStore{}
			if tc.err != nil {
				store.add(t, experimentFixture("run", time.Now().UTC()))
			}
			if tc.list {
				store.listErr = tc.err
			} else if tc.name == "close failed" {
				store.closeErr = tc.err
			} else {
				store.openErr = tc.err
			}
			service := &experimentService{reader: store, lister: store, store: "evidence", maxBytes: 1 << 20}
			_, err := service.get(context.Background(), "run")
			var domain *serving.Error
			missing := errors.As(err, &domain) && domain.Code == "NotFound"
			if tc.err == nil || errors.Is(tc.err, evidencestore.ErrNotFound) {
				if !missing {
					t.Fatalf("missing not classified: %v", err)
				}
			} else if missing || !errors.Is(err, tc.err) {
				t.Fatalf("store failure hidden: %v", err)
			}
		})
	}
	store := &experimentMemoryStore{}
	service := &experimentService{reader: store, lister: store, store: "evidence", maxBytes: 1 << 20}
	list, err := service.list(context.Background())
	if err != nil || list.Items == nil || len(list.Items) != 0 {
		t.Fatalf("empty list=%+v err=%v", list, err)
	}
}

func TestExperimentSummaryValidatesStoredBoundaryAndDoesNotReadRaw(t *testing.T) {
	for _, name := range []string{"identity", "stage interval", "schema", "source store", "source traversal", "source URL", "source absolute", "KPI null", "unknown raw field", "trailing JSON", "oversize", "actual oversize"} {
		t.Run(name, func(t *testing.T) {
			summary := experimentFixture("run", time.Now().UTC())
			switch name {
			case "stage interval":
				summary.Stages[0].To = summary.To.Add(time.Second)
			case "schema":
				summary.SchemaVersion = "raw.v1"
			case "source store":
				summary.Sources[0].Store = "unconfigured"
			case "source traversal":
				summary.Sources[0].Key = "../private"
			case "source URL":
				summary.Sources[0].Key = "https://host/private"
			case "source absolute":
				summary.Sources[0].Key = "/tmp/private"
			case "KPI null":
				summary.Stages[0].KPIs = json.RawMessage(`null`)
			}
			store := &experimentMemoryStore{}
			key := store.add(t, summary)
			service := &experimentService{reader: store, lister: store, store: "evidence", maxBytes: 1 << 20}
			switch name {
			case "identity":
				store.objects[0].Store = "other-store"
			case "unknown raw field":
				store.bodies[key] = strings.TrimSuffix(store.bodies[key], "}") + `,"raw":"secret"}`
			case "trailing JSON":
				store.bodies[key] += `{}`
			case "oversize":
				service.maxBytes = store.objects[0].Size - 1
			case "actual oversize":
				service.maxBytes = store.objects[0].Size + 1
				store.bodies[key] += strings.Repeat(" ", 8)
			}
			if _, err := service.get(context.Background(), "run"); err == nil {
				t.Fatal("invalid immutable summary accepted")
			}
			for _, opened := range store.opened {
				if opened != key {
					t.Fatalf("reader followed raw evidence: %s", opened)
				}
			}
		})
	}
}

func TestExperimentSummaryCancellationAndSafeIDs(t *testing.T) {
	store := &experimentMemoryStore{}
	store.add(t, experimentFixture("run", time.Now().UTC()))
	service := &experimentService{reader: store, lister: store, store: "evidence", maxBytes: 1 << 20}
	for _, id := range []string{"..", "../run", "a/b", "a\\b", "%2F", "https:host", ".hidden"} {
		if _, err := service.get(context.Background(), id); err == nil {
			t.Fatalf("unsafe id accepted: %s", id)
		}
	}
	if len(store.prefixes) != 0 {
		t.Fatal("unsafe id reached store")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.get(ctx, "run"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if len(store.opened) != 0 || len(store.contexts) != 1 || store.contexts[0] != ctx {
		t.Fatal("request context not propagated before reading")
	}
}
