package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
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
		Stages: []httpapi.ExperimentStage{{
			ID: "full-E5-2", From: from, To: from.Add(time.Minute),
			KPIs: json.RawMessage(`{
				"visibility":"PRIVATE_OPERATIONAL_ONLY","status":"FAIL",
				"requests":{"offered":5,"outcomes":{"completed":3,"rejected":2},
					"success":{"numerator":3,"denominator":5},"slo":{"numerator":2,"denominator":5},
					"completedLatencyNS":{"p95":17}},
				"wholePoolJoules":null,"activePoolJoules":null,"reasons":["power missing"],
				"population":"offered requests in [from,to); pool energy shared across workloads"
			}`),
		}},
		Sources: []httpapi.ExperimentSource{{Store: "evidence", Key: "serving/observations/hour/requests.jsonl.gz"}},
	}
}

func TestExperimentSummaryReadPreservesPublishedEvidenceAndSorts(t *testing.T) {
	store := &experimentMemoryStore{}
	from := time.Date(2026, 10, 24, 1, 0, 0, 123, time.UTC)
	store.add(t, experimentFixture("older", from.Add(-time.Hour)))
	store.add(t, experimentFixture("z", from))
	withDefinition := experimentFixture("a", from)
	withDefinition.Definition = json.RawMessage(
		`{"spec":{"comparisons":[{"left":{"stageID":"baseline"},"future":true}]},"future":{"opaque":true}}`)
	withDefinition.Outcome = json.RawMessage(`{"execution":{"status":"Complete"},"future":true}`)
	store.add(t, withDefinition)
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
	if len(list.Items) != 3 || list.Items[0].ID != "a" || list.Items[1].ID != "z" ||
		list.Items[2].ID != "older" || len(store.opened) != 3 {
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
	assertPublicationFields(t, detail, withDefinition, list)
	var kpis map[string]any
	if err := json.Unmarshal(detail.Stages[0].KPIs, &kpis); err != nil {
		t.Fatal(err)
	}
	if kpis["wholePoolJoules"] != nil || kpis["activePoolJoules"] != nil || kpis["status"] != "FAIL" ||
		kpis["visibility"] != "PRIVATE_OPERATIONAL_ONLY" || len(kpis["reasons"].([]any)) != 1 {
		t.Fatalf("missing/failed/private interpretations changed: %v", kpis)
	}
	requests := kpis["requests"].(map[string]any)
	if requests["offered"] != float64(5) || requests["slo"].(map[string]any)["numerator"] != float64(2) ||
		requests["slo"].(map[string]any)["denominator"] != float64(5) {
		t.Fatalf("KPI populations changed: %v", requests)
	}
}

func TestExperimentListRetainsFrozenFactsWithoutDefinition(t *testing.T) {
	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	recorded := experimentFixture("recorded", from)
	recorded.Definition = json.RawMessage(`{
		"run":{"name":"recorded-name","uid":"recorded"},
		"spec":{"question":"recorded question","settingsPatch":{"minimumDemand":{"x":1}},
			"control":{"controls":{"power":false},"future":true},
			"stages":[{"id":"full-E5-2","row":"E5-2","mode":"controls-disabled",
				"settingsPatch":{"controls":{"power":true}},"control":{"controls":{"power":true}},
				"loadScenarioRef":{"name":"frozen-load","uid":"load-old"},"failureInjection":[{"id":"omit"}]}]},
		"workloads":[{"name":"service","uid":"workload-old","generation":9007199254740993,
			"slo":{"percentile":95,"targetLatencyNanos":9007199254740993},"model":{"large":"omit"}}],
		"loadScenarios":[{"name":"frozen-load","uid":"load-old","spec":{"parts":[{"large":"omit"}]}}]
	}`)
	store := &experimentMemoryStore{}
	store.add(t, recorded)
	store.add(t, experimentFixture("historical-without-definition", from.Add(-time.Hour)))
	service := &experimentService{reader: store, lister: store, store: "evidence", maxBytes: 1 << 20}
	list, err := service.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	item := list.Items[0]
	facts := item.Facts
	if len(item.Definition) != 0 || facts == nil ||
		facts.Name != "recorded-name" || facts.Question != "recorded question" ||
		len(facts.Stages) != 1 ||
		!reflect.DeepEqual(facts.Stages[0].SettingsPatchKeys, []string{"controls.power"}) ||
		string(facts.Stages[0].LoadScenarioRef) != `{"name":"frozen-load","uid":"load-old"}` ||
		len(facts.SLOSources) != 1 || facts.SLOSources[0].UID != "workload-old" ||
		string(facts.SLOSources[0].Generation) != "9007199254740993" ||
		string(facts.SLOSources[0].SLO) != `{"percentile":95,"targetLatencyNanos":9007199254740993}` ||
		len(facts.LoadSources) != 1 || facts.LoadSources[0].UID != "load-old" {
		t.Fatalf("frozen facts lost or changed after definition projection: %+v", item)
	}
	body, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"definition"`) || strings.Contains(string(body), `"omit"`) ||
		strings.Contains(string(body), `"mode"`) || strings.Contains(string(body), `"control"`) ||
		strings.Count(string(body), `"facts"`) != 1 || list.Items[1].Facts != nil {
		t.Fatalf("list leaked full definition or invented historical metadata: %s", body)
	}
	wantDefinition, err := json.Marshal(recorded.Definition)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := service.get(context.Background(), recorded.ID)
	if err != nil || string(detail.Definition) != string(wantDefinition) || detail.Facts != nil {
		t.Fatalf("immutable stored summary changed: %+v err=%v", detail, err)
	}
}

func TestExperimentMetadataIsOpaqueForBothPublishersAndHistoricalSummaries(t *testing.T) {
	for _, shape := range []string{"requests", "workloads", "historical-213"} {
		t.Run(shape, func(t *testing.T) {
			summary := experimentFixture("new-record", time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC))
			if shape != "historical-213" {
				summary.Stages[0].KPIs = json.RawMessage(`{"` + shape + `":{"recorded-name":{
					"offered":5,"successNumerator":0,"successDenominator":5,
					"workloadUID":"historical-uid","definitionID":"historical-uid/1/model/2/preprocessor/3",
					"identitySource":{"store":"evidence","key":"serving/experiments.v1alpha1/new-record/raw/frozen/scheduler.json"},
					"substages":[{"id":"low","alpha":0.3,"startOffsetNanos":0,"endOffsetNanos":1000000001,
						"from":"2026-10-30T00:00:00.000000001Z","to":"2026-10-30T00:00:01.000000002Z",
						"source":{"store":"evidence","key":"serving/experiments.v1alpha1/new-record/raw/stage/phase.json"}},
						{"id":"direct","alpha":null,"startOffsetNanos":1000000001,"endOffsetNanos":2000000002,
						"from":null,"to":null,"source":null}],
					"missingReasons":{"alpha":"Direct rates","producerAnchor":"Not recorded"}}},
					"wholePoolJoules":null,"activePoolJoules":0,
					"limitations":["Scheduled producer-relative intervals, not observed events"]}`)
				summary.Sources = []httpapi.ExperimentSource{
					{Store: "evidence", Key: "serving/experiments.v1alpha1/new-record/raw/frozen/scheduler.json"},
					{Store: "evidence", Key: "serving/experiments.v1alpha1/new-record/raw/stage/phase.json"},
				}
			} else {
				summary.ID = "control-213"
			}
			store := &experimentMemoryStore{}
			key := store.add(t, summary)
			service := &experimentService{reader: store, lister: store, store: "evidence", maxBytes: 1 << 20}
			detail, err := service.get(context.Background(), summary.ID)
			if err != nil {
				t.Fatal(err)
			}
			list, err := service.list(context.Background())
			if err != nil || len(list.Items) != 1 {
				t.Fatalf("list=%+v err=%v", list, err)
			}
			var want any
			if err := json.Unmarshal(summary.Stages[0].KPIs, &want); err != nil {
				t.Fatal(err)
			}
			for _, value := range []httpapi.ExperimentSummary{detail, list.Items[0]} {
				var got any
				if err := json.Unmarshal(value.Stages[0].KPIs, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(value.Sources, summary.Sources) {
					t.Fatalf("opaque metadata/populations/units/sources changed: %+v", value)
				}
			}
			if len(store.opened) != 2 || store.opened[0] != key || store.opened[1] != key {
				t.Fatalf("GET followed raw sources instead of reading only summary: %v", store.opened)
			}
		})
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
			list, err := service.list(context.Background())
			if tc.err == nil {
				if err != nil || list.Items == nil || len(list.Items) != 0 {
					t.Fatalf("missing publication list=%+v err=%v", list, err)
				}
			} else if !errors.Is(err, tc.err) {
				t.Fatalf("list store failure changed: %v", err)
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
	for _, name := range []string{
		"identity", "stage interval", "schema", "source store", "source traversal", "source URL",
		"source absolute", "KPI null", "unknown raw field", "trailing JSON", "oversize", "actual oversize",
		"definition scalar", "definition array", "definition null",
		"outcome scalar", "outcome array", "outcome null",
	} {
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
			case "definition scalar":
				summary.Definition = json.RawMessage(`42`)
			case "definition array":
				summary.Definition = json.RawMessage(`[]`)
			case "definition null":
				summary.Definition = json.RawMessage(`null`)
			case "outcome scalar":
				summary.Outcome = json.RawMessage(`42`)
			case "outcome array":
				summary.Outcome = json.RawMessage(`[]`)
			case "outcome null":
				summary.Outcome = json.RawMessage(`null`)
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

func assertPublicationFields(
	t *testing.T, detail, withDefinition httpapi.ExperimentSummary, list httpapi.ExperimentList,
) {
	t.Helper()
	if string(detail.Definition) != string(withDefinition.Definition) ||
		len(list.Items[0].Definition) != 0 ||
		len(list.Items[1].Definition) != 0 || len(list.Items[2].Definition) != 0 {
		t.Fatalf("optional definition changed: detail=%s list=%+v", detail.Definition, list.Items)
	}
	if string(detail.Outcome) != string(withDefinition.Outcome) ||
		string(list.Items[0].Outcome) != string(withDefinition.Outcome) ||
		len(list.Items[1].Outcome) != 0 || len(list.Items[2].Outcome) != 0 {
		t.Fatalf("optional outcome changed: detail=%s list=%+v", detail.Outcome, list.Items)
	}
	if string(list.Items[0].Comparisons) != `[{"left":{"stageID":"baseline"},"future":true}]` ||
		len(detail.Comparisons) != 0 || len(list.Items[1].Comparisons) != 0 || len(list.Items[2].Comparisons) != 0 {
		t.Fatalf("list-only comparison projection changed: detail=%+v list=%+v", detail, list.Items)
	}
}

func TestPublicationSummaryConcurrentS3ReadsAreRequestScoped(t *testing.T) {
	evaluations, request := evaluationFixture(t, nil)
	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	older := experimentFixture("older", from)
	definition, err := json.Marshal(struct {
		Spec any `json:"spec"`
	}{request.Spec})
	if err != nil {
		t.Fatal(err)
	}
	older.Definition = definition
	newest := experimentFixture("newest-without-definition", from.Add(time.Hour))
	objects := &experimentMemoryStore{}
	olderKey := objects.add(t, older)
	newestKey := objects.add(t, newest)

	var mu sync.Mutex
	lists := 0
	reads := map[string]int{}
	server := httptest.NewServer(publicationS3Handler(t, objects, &mu, &lists, reads))
	t.Cleanup(server.Close)
	storage, err := stores3.New(stores3.Config{
		LogicalStore: "evidence", Bucket: "bucket", Endpoint: server.URL, Region: "region",
		AccessKeyID: "test", SecretAccessKey: "test", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	experiments := &experimentService{
		reader: storage, lister: storage, store: "evidence", maxBytes: 1 << 20,
	}
	var config httpapi.HandlerConfig
	configureEvaluationTemplate(&config, evaluations, "evidence", experiments)

	const pairs = 8
	start := make(chan struct{})
	failures := make(chan error, pairs*2)
	var workers sync.WaitGroup
	for range pairs {
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			result, err := config.GetEvaluationTemplate(context.Background())
			if err != nil {
				failures <- err
			} else if result.Sources["harness"] != "latestPublication" ||
				result.SourcePublication == nil || result.SourcePublication.UID != older.ID ||
				result.Spec.Question != request.Spec.Question {
				failures <- fmt.Errorf("template lost its publication definition: %+v", result)
			}
		}()
		go func() {
			defer workers.Done()
			<-start
			result, err := experiments.list(context.Background())
			if err != nil {
				failures <- err
			} else if len(result.Items) != 2 || result.Items[0].ID != newest.ID ||
				len(result.Items[0].Definition) != 0 || len(result.Items[1].Definition) != 0 {
				failures <- fmt.Errorf("list projection changed: %+v", result)
			}
		}()
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	mu.Lock()
	gotLists, gotReads := lists, reads[olderKey]+reads[newestKey]
	keysRead := len(reads)
	mu.Unlock()
	if gotLists != pairs*2 || gotReads != pairs*4 || keysRead != 2 {
		t.Fatalf("duplicate S3 reads: LIST=%d GET=%d keys=%d, want %d/%d/2",
			gotLists, gotReads, keysRead, pairs*2, pairs*4)
	}

	fresh := experimentFixture("fresh", from.Add(2*time.Hour))
	request.Spec.Question = "fresh publication"
	fresh.Definition, err = json.Marshal(struct {
		Spec any `json:"spec"`
	}{request.Spec})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	freshKey := objects.add(t, fresh)
	mu.Unlock()
	result, err := config.GetEvaluationTemplate(context.Background())
	if err != nil || result.Sources["harness"] != "latestPublication" ||
		result.SourcePublication == nil || result.SourcePublication.UID != fresh.ID ||
		result.Spec.Question != request.Spec.Question {
		t.Fatalf("new publication hidden by request reuse: result=%+v err=%v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if lists != pairs*2+1 || reads[olderKey] != pairs*2+1 ||
		reads[newestKey] != pairs*2+1 || reads[freshKey] != 1 || len(reads) != 3 {
		t.Fatalf("later request did not read current summaries once: LIST=%d GET=%v", lists, reads)
	}
}

func publicationS3Handler(
	t *testing.T, objects *experimentMemoryStore, mu *sync.Mutex, lists *int, reads map[string]int,
) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("publication read mutated storage: %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("list-type") == "2" {
			(*lists)++
			if (r.URL.Path != "/bucket" && r.URL.Path != "/bucket/") ||
				r.URL.Query().Get("prefix") != httpapi.ExperimentPrefix {
				t.Errorf("unexpected publication listing: %s", r.URL)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			type listedObject struct {
				Key  string `xml:"Key"`
				Size int    `xml:"Size"`
			}
			listing := struct {
				XMLName  xml.Name       `xml:"ListBucketResult"`
				Contents []listedObject `xml:"Contents"`
			}{}
			for key, body := range objects.bodies {
				listing.Contents = append(listing.Contents, listedObject{Key: key, Size: len(body)})
			}
			w.Header().Set("Content-Type", "application/xml")
			if err := xml.NewEncoder(w).Encode(listing); err != nil {
				t.Errorf("write S3 listing: %v", err)
			}
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		reads[key]++
		body, ok := objects.bodies[key]
		if !ok || key == r.URL.Path {
			t.Errorf("read followed non-summary evidence: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

func TestExperimentSettingsPatchKeysUseRecordedInheritance(t *testing.T) {
	for _, test := range []struct {
		name, runPatch, stagePatch string
		want                       []string
	}{
		{"no patch", "", "", []string{}},
		{
			name:     "inherited",
			runPatch: `"settingsPatch":{"minimumDemand":{"x":1},"controls":{"power":false}},`,
			want:     []string{"controls.power", "minimumDemand.x"},
		},
		{
			name:       "override",
			runPatch:   `"settingsPatch":{"controls":{"power":false}},`,
			stagePatch: `"settingsPatch":{"controls":{"routing":false}},`,
			want:       []string{"controls.routing"},
		},
		{
			name:       "empty override",
			runPatch:   `"settingsPatch":{"controls":{"power":false}},`,
			stagePatch: `"settingsPatch":{},`,
			want:       []string{},
		},
		{
			name:       "null inherits",
			runPatch:   `"settingsPatch":{"controls":{"power":false}},`,
			stagePatch: `"settingsPatch":null,`,
			want:       []string{"controls.power"},
		},
		{"unknown patch shape", `"settingsPatch":false,`, "", nil},
		{
			name:       "leaves",
			stagePatch: `"settingsPatch":{"minimumDemand":{"x":null},"controls":{}},`,
			want:       []string{"controls", "minimumDemand.x"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := experimentFixture("recorded", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
			recorded.Definition = json.RawMessage(fmt.Sprintf(
				`{"spec":{%s"stages":[{"id":"stage",%s"row":"recorded"}]}}`, test.runPatch, test.stagePatch))
			store := &experimentMemoryStore{}
			store.add(t, recorded)
			service := &experimentService{reader: store, lister: store, store: "evidence", maxBytes: 1 << 20}
			list, err := service.list(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			fact := list.Items[0].Facts.Stages[0]
			if !reflect.DeepEqual(fact.SettingsPatchKeys, test.want) {
				t.Fatalf("keys=%v want=%v", fact.SettingsPatchKeys, test.want)
			}
			raw, err := json.Marshal(fact)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(test.want)
			if err != nil {
				t.Fatal(err)
			}
			if string(fields["settingsPatchKeys"]) != string(want) {
				t.Fatalf("nullable key projection lost: %s", raw)
			}
		})
	}
}
