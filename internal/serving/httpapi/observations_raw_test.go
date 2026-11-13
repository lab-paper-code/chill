package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type rawHTTPArchive struct {
	settingsArchive
	objects   []evidencestore.ObjectIdentity
	body      []byte
	list      func(context.Context) error
	openError error
	opens     int
}

func (a *rawHTTPArchive) List(ctx context.Context, _ string) ([]evidencestore.ObjectIdentity, error) {
	if a.list != nil {
		if err := a.list(ctx); err != nil {
			return nil, err
		}
	}
	return a.objects, nil
}
func (a *rawHTTPArchive) Open(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	a.opens++
	if a.openError != nil {
		return nil, a.openError
	}
	return io.NopCloser(bytes.NewReader(a.body)), nil
}

func rawHTTPHandler(t *testing.T, archive *rawHTTPArchive, allowed bool) *Handler {
	t.Helper()
	return rawHTTPHandlerWithClock(t, archive, allowed, nil)
}

func rawHTTPHandlerWithClock(t *testing.T, archive *rawHTTPArchive, allowed bool, now func() time.Time) *Handler {
	t.Helper()
	archive.settingsArchive.directory = t.TempDir()
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour, Writer: archive, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: "collector"}}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		sar := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		if sar.Spec.NonResourceAttributes == nil || !strings.HasPrefix(sar.Spec.NonResourceAttributes.Path, "/chill/api/v1/observations/") {
			t.Errorf("wrong raw SAR: %+v", sar.Spec)
		}
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	h := &Handler{observationLog: log, authorizer: &servingkube.Authorizer{Client: kube}, mux: http.NewServeMux(), config: HandlerConfig{RequestTimeout: 50 * time.Millisecond, OperationTimeout: 200 * time.Millisecond, MaxQueryEvents: 10}}
	h.mux.HandleFunc("GET /api/v1/observations/scheduler", h.schedulerObservations)
	return h
}

func rawHTTPRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer collector-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestRawHTTPStrictPathsAndAuthorization(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	for _, path := range []string{
		rawObservationsPath, rawObservationsPath + "?hour=20261030T10&hour=20261030T11",
		rawObservationsPath + "?hour=20261030T10&kind=requests", rawObservationsPath + "?hour=20261030T24",
		rawObservationsPath + "/20261030T10/bad", rawObservationsPath + "/20261030T10/requests/extra",
		rawObservationsPath + "/definitions?extra=1", rawObservationsPath + "/../definitions",
		rawObservationsPath + "/20261030T10%2Frequests/requests", rawObservationsPath + "/",
	} {
		if response := rawHTTPRequest(h, path); response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe raw request %s: %d %s", path, response.Code, response.Body)
		}
	}
	if response := rawHTTPRequest(h, rawObservationsPath+"/20000101T00/requests"); response.Code != http.StatusNotFound {
		t.Fatalf("missing stream: %d %s", response.Code, response.Body)
	}
	if response := rawHTTPRequest(h, rawObservationsPath+"/"+time.Now().UTC().Format("20060102T15")+"/requests"); response.Code != http.StatusConflict {
		t.Fatalf("open stream: %d %s", response.Code, response.Body)
	}
	request := httptest.NewRequest(http.MethodGet, rawObservationsPath+"/definitions", nil)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("raw bypassed bearer auth: %d", response.Code)
	}
	denied := rawHTTPHandler(t, &rawHTTPArchive{}, false)
	if response := rawHTTPRequest(denied, rawObservationsPath+"/definitions"); response.Code != http.StatusForbidden {
		t.Fatalf("raw bypassed SAR: %d", response.Code)
	}
}

func TestRawHTTPStoredGzipAndTransferFailure(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte("{\"preserve\": true}\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	hour := time.Now().UTC().Add(-4 * time.Hour).Format("20060102T15")
	archive := &rawHTTPArchive{body: compressed.Bytes(), objects: []evidencestore.ObjectIdentity{{Store: "evidence", Key: "serving/observations.v1alpha7/" + hour + "/scheduler.jsonl.gz", Size: int64(compressed.Len())}}}
	h := rawHTTPHandler(t, archive, true)
	response := rawHTTPRequest(h, rawObservationsPath+"/"+hour+"/scheduler")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/gzip" || response.Header().Get("Content-Encoding") != "" || response.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(response.Body.Bytes(), compressed.Bytes()) {
		t.Fatalf("gzip changed: %d %v %q", response.Code, response.Header(), response.Body.Bytes())
	}
	archive.openError = errors.New("store failed")
	if response := rawHTTPRequest(h, rawObservationsPath+"/"+hour+"/scheduler"); response.Code < 500 {
		t.Fatalf("store error hidden: %d", response.Code)
	}
	archive.openError = nil
	archive.body = archive.body[:len(archive.body)-1]
	server := httptest.NewServer(h)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+rawObservationsPath+"/"+hour+"/scheduler", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer collector-token")
	result, err := server.Client().Do(request)
	if err == nil {
		_, readErr := io.ReadAll(result.Body)
		_ = result.Body.Close()
		if readErr == nil {
			t.Fatal("truncated gzip transfer looked complete")
		}
	}
}

func TestRawHTTPAndSchedulerSeparateProcessingAndWriteDeadlines(t *testing.T) {
	for _, path := range []string{rawObservationsPath + "?hour=20000101T00", "/api/v1/observations/scheduler?from=2000-01-01T00:00:00Z&to=2000-01-01T01:00:00Z"} {
		t.Run(path, func(t *testing.T) {
			archive := &rawHTTPArchive{}
			h := rawHTTPHandler(t, archive, true)
			archive.list = func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= h.config.RequestTimeout {
					t.Errorf("historical read used short request deadline")
				}
				<-ctx.Done()
				return ctx.Err()
			}
			server := httptest.NewServer(h)
			defer server.Close()
			request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer collector-token")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			var body serving.Error
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil || response.StatusCode != http.StatusGatewayTimeout || body.Code != requestTimeoutCode {
				t.Fatalf("processing timeout could not be delivered: %d %+v %v", response.StatusCode, body, err)
			}
		})
	}
}

func TestRawHTTPEmptyDefinitions(t *testing.T) {
	archive := &rawHTTPArchive{}
	h := rawHTTPHandler(t, archive, true)
	response := rawHTTPRequest(h, rawObservationsPath+"/definitions")
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") != "0" || response.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("empty definitions: %d %v %q", response.Code, response.Header(), response.Body.String())
	}
}

func TestSchedulerHistorySelectionQueries(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	from := time.Now().UTC().Add(-time.Second)
	to := from.Add(time.Minute)
	for _, event := range []observation.SchedulerObservation{
		{At: from.Add(3 * time.Second), Action: &observation.SchedulerActionObservation{ID: "new", Expected: json.RawMessage(`{"workloadUID":"w"}`)}},
		{At: from, Snapshot: &serving.SchedulerSnapshot{Workloads: []*serving.Definition{{UID: "w", Generation: 1}}}},
		{At: from.Add(time.Second), Decision: &serving.SchedulerDecision{Status: "old", Workloads: []serving.SchedulerWorkloadState{{UID: "w"}}}},
		{At: from.Add(4 * time.Second), Action: &observation.SchedulerActionObservation{ID: "other", Expected: json.RawMessage(`{"workloadUID":"other"}`)}},
	} {
		if err := h.observationLog.SchedulerRecord(event); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/v1/observations/scheduler?from=" + from.Format(time.RFC3339Nano) + "&to=" + to.Format(time.RFC3339Nano)
	for _, view := range []string{"", "&view=compact", "&view=summary"} {
		for cursor, original := range []int{0, 2} {
			path := base + "&limit=1&order=desc&filter=decision-action&uid=w&cursor=" + strconv.Itoa(cursor) + view
			response := rawHTTPRequest(h, path)
			var result struct {
				Events     []observation.SchedulerObservation
				NextCursor string
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK ||
				len(result.Events) != 1 || result.Events[0].Offset == nil || *result.Events[0].Offset != original {
				t.Fatalf("history selection: status=%d body=%s err=%v", response.Code, response.Body, err)
			}
			if cursor == 0 && result.NextCursor != "1" || cursor == 1 && result.NextCursor != "" {
				t.Fatalf("history next cursor: %s", response.Body)
			}
		}
	}
	for _, invalid := range []string{
		"order=", "order=DESC", "order=asc&order=desc", "filter=", "filter=wrong", "filter=action&filter=decision",
		"uid=", "uid=a&uid=b", "uid=a%20b", "uid=a%2Fb", "uid=a%5Cb", "uid=a%00b", "uid=" + strings.Repeat("a", 257),
	} {
		response := rawHTTPRequest(h, base+"&"+invalid)
		var result serving.Error
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusBadRequest || result.Code != "InvalidQuery" {
			t.Fatalf("invalid history query %s: status=%d body=%s err=%v", invalid, response.Code, response.Body, err)
		}
	}
}

func TestSchedulerHistoryDecisionChangeFilterPagesOnlyChangedDecisions(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	from := time.Now().UTC().Add(-time.Second)
	to := from.Add(time.Minute)
	keep := serving.SchedulerCandidate{ID: "keep"}
	plan := func(id string) *serving.SchedulerCandidate { return &serving.SchedulerCandidate{ID: id} }
	for i, decision := range []serving.SchedulerDecision{
		{Status: serving.SchedulerStatusKeep, Keep: keep, Target: plan("keep")},
		{Status: "Feasible", Keep: keep, Target: plan("plan-a")},
		{Status: "Pending", Keep: keep},
		{Status: serving.SchedulerStatusOverload, Keep: keep, Target: plan("keep")},
		{Status: serving.SchedulerStatusOverload, Keep: keep, Target: plan("plan-b")},
		{Status: serving.SchedulerStatusKeep, Keep: keep, Target: plan("keep")},
		{Status: "Feasible", Keep: keep, Target: plan("plan-c")},
	} {
		if err := h.observationLog.SchedulerRecord(observation.SchedulerObservation{
			At: from.Add(time.Duration(i) * time.Second), Decision: &decision,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.observationLog.SchedulerRecord(observation.SchedulerObservation{At: from.Add(7 * time.Second), Action: &observation.SchedulerActionObservation{ID: "action"}}); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/observations/scheduler?from=" + from.Format(time.RFC3339Nano) + "&to=" + to.Format(time.RFC3339Nano) + "&filter=decision-change&limit=2&view=summary"
	for order, want := range map[string][]int{"asc": {1, 4, 6}, "desc": {6, 4, 1}} {
		var got []int
		cursor := ""
		for page := 0; page == 0 || cursor != ""; page++ {
			if page > 2 {
				t.Fatalf("%s pages did not terminate: %v", order, got)
			}
			response := rawHTTPRequest(h, base+"&order="+order+cursor)
			var result struct {
				Events     []observation.SchedulerObservation
				NextCursor string
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
				t.Fatalf("%s decision-change: status=%d body=%s err=%v", order, response.Code, response.Body, err)
			}
			for _, event := range result.Events {
				if event.Decision == nil || event.Offset == nil {
					t.Fatalf("%s selected a non-change: %+v", order, event)
				}
				got = append(got, *event.Offset)
			}
			cursor = ""
			if result.NextCursor != "" {
				cursor = "&cursor=" + result.NextCursor
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s changed decisions skipped or repeated: got %v want %v", order, got, want)
		}
	}
}

func TestSchedulerHistorySummaryCarriesDecisionReasonsTargetKeepAndEvaluationTime(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	from := time.Now().UTC().Add(-time.Second)
	keepJoules, planJoules := 120.5, 80.25
	keep := serving.SchedulerCandidate{ID: "keep", Keep: true, ExpectedJoules: &keepJoules}
	for i, decision := range []serving.SchedulerDecision{
		{Status: serving.SchedulerStatusKeep, Keep: keep, Target: &serving.SchedulerCandidate{ID: "keep", Keep: true}, Reasons: []string{"TransitionGainWithinUncertainty"}, EvaluationElapsed: 1250 * time.Millisecond},
		{Status: "Feasible", Keep: keep, Target: &serving.SchedulerCandidate{ID: "plan-a", ExpectedJoules: &planJoules}, Reasons: []string{"IdleInstanceRemoved", "ReservedScope:w"}, EvaluationElapsed: 7},
		{Status: "Pending", Reasons: []string{"PartialObservationWindow"}},
	} {
		if err := h.observationLog.SchedulerRecord(observation.SchedulerObservation{At: from.Add(time.Duration(i) * time.Second), Decision: &decision}); err != nil {
			t.Fatal(err)
		}
	}
	window := "/api/v1/observations/scheduler?from=" + from.Format(time.RFC3339Nano) + "&to=" + from.Add(time.Minute).Format(time.RFC3339Nano) + "&limit=10"
	response := rawHTTPRequest(h, window+"&view=summary")
	var page struct {
		Events []struct {
			Decision map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Events) != 3 {
		t.Fatalf("summary: status=%d body=%s err=%v", response.Code, response.Body, err)
	}
	var full struct {
		Events []observation.SchedulerObservation
	}
	if err := json.Unmarshal(rawHTTPRequest(h, window).Body.Bytes(), &full); err != nil || len(full.Events) != 3 {
		t.Fatalf("full read: %v", err)
	}
	for i, want := range []struct{ reasons, targetKeep, elapsed, keepJoules, targetJoules string }{
		{`["TransitionGainWithinUncertainty"]`, `true`, `1250000000`, `120.5`, `120.5`},
		{`["IdleInstanceRemoved","ReservedScope:w"]`, `false`, `7`, `120.5`, `80.25`},
		{`["PartialObservationWindow"]`, ``, ``, ``, ``},
	} {
		decision := page.Events[i].Decision
		if string(decision["reasons"]) != want.reasons || string(decision["targetKeep"]) != want.targetKeep || string(decision["evaluationElapsedNanos"]) != want.elapsed {
			t.Fatalf("decision %d lost its reasons, target identity or evaluation time: %s", i, response.Body)
		}
		if string(decision["keepExpectedJoules"]) != want.keepJoules || string(decision["targetExpectedJoules"]) != want.targetJoules {
			t.Fatalf("decision %d expected energy: %s", i, response.Body)
		}
		stored := full.Events[i].Decision
		if stored.Keep.ExpectedJoules != nil && fmt.Sprint(*stored.Keep.ExpectedJoules) != want.keepJoules ||
			stored.Target != nil && stored.Target.ExpectedJoules != nil && fmt.Sprint(*stored.Target.ExpectedJoules) != want.targetJoules {
			t.Fatalf("decision %d summary differs from the stored record: %+v", i, stored)
		}
	}
}

func TestSchedulerHistorySummaryCarriesActionWorkload(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	from := time.Now().UTC().Add(-time.Second)
	evidence := strings.Repeat("x", 64<<10)
	for i, expected := range []string{
		`{"workloadUID":"w","target":{"reasons":["` + evidence + `"]}}`,
		`{"prepare":{"owner":{"name":"svc","uid":"owner"}}}`,
		`{"workloadUIDs":["a","b"]}`,
		``,
	} {
		action := &observation.SchedulerActionObservation{ID: "a" + strconv.Itoa(i), Kind: "prepare", Phase: "running", StartedAt: from}
		if expected != "" {
			action.Expected = json.RawMessage(expected)
		}
		if err := h.observationLog.SchedulerRecord(observation.SchedulerObservation{At: from.Add(time.Duration(i) * time.Second), Action: action}); err != nil {
			t.Fatal(err)
		}
	}
	response := rawHTTPRequest(h, "/api/v1/observations/scheduler?from="+from.Format(time.RFC3339Nano)+"&to="+from.Add(time.Minute).Format(time.RFC3339Nano)+"&limit=10&view=summary")
	var page struct {
		Events []struct {
			Action map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Events) != 4 || response.Body.Len() > 4096 {
		t.Fatalf("summary: status=%d bytes=%d err=%v", response.Code, response.Body.Len(), err)
	}
	for i, want := range []struct{ uid, uids string }{{`"w"`, ``}, {`"owner"`, ``}, {``, `["a","b"]`}, {``, ``}} {
		action := page.Events[i].Action
		if string(action["workloadUID"]) != want.uid || string(action["workloadUIDs"]) != want.uids {
			t.Fatalf("action %d workload: %s", i, response.Body)
		}
	}
}

func TestWorkloadObservationOrderPagesNewestFirstWithUnchangedAggregates(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	h.mux.HandleFunc("GET /api/v1/observations/workloads/{uid}", h.observations)
	from := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	for i := range 5 {
		id, at := "r"+strconv.Itoa(i), from.Add(time.Duration(i)*time.Second)
		for _, event := range []serving.RequestEvent{
			{Kind: "received", At: at},
			{Kind: "response_finished", At: at.Add(time.Duration(i+1) * time.Millisecond), ElapsedNanos: int64(time.Duration(i+1) * time.Millisecond), Outcome: "completed"},
		} {
			event.RequestID, event.WorkloadUID, event.ReceivedAt = id, "w", at
			if err := h.observationLog.RequestEvent(event); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.observationLog.RequestClosed(id, serving.RequestTimings{}); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/v1/observations/workloads/w?from=" + from.Format(time.RFC3339Nano) + "&to=" + from.Add(time.Minute).Format(time.RFC3339Nano) + "&limit=2"
	type page struct {
		Total, Selected                            int
		Outcomes, Cohorts, Definitions, Management json.RawMessage
		Requests                                   []struct{ RequestID string }
		NextCursor                                 string
	}
	read := func(order string) ([]string, []page) {
		var ids []string
		var pages []page
		for cursor := ""; len(pages) == 0 || cursor != ""; {
			if len(pages) > 3 {
				t.Fatalf("order %q pages did not end: %v", order, ids)
			}
			response := rawHTTPRequest(h, base+order+cursor)
			var p page
			if err := json.Unmarshal(response.Body.Bytes(), &p); err != nil || response.Code != http.StatusOK {
				t.Fatalf("order %q: status=%d body=%s err=%v", order, response.Code, response.Body, err)
			}
			for _, row := range p.Requests {
				ids = append(ids, row.RequestID)
			}
			pages = append(pages, p)
			cursor = ""
			if p.NextCursor != "" {
				cursor = "&cursor=" + p.NextCursor
			}
		}
		return ids, pages
	}
	ascending, ascPages := read("&order=asc")
	byDefault, _ := read("")
	descending, descPages := read("&order=desc")
	if !slices.Equal(ascending, []string{"r0", "r1", "r2", "r3", "r4"}) || !slices.Equal(byDefault, ascending) {
		t.Fatalf("ascending/default rows: asc=%v default=%v", ascending, byDefault)
	}
	if !slices.Equal(descending, []string{"r4", "r3", "r2", "r1", "r0"}) {
		t.Fatalf("descending rows skipped, repeated or not newest first: %v", descending)
	}
	for i, p := range descPages {
		a := ascPages[i]
		if p.Total != a.Total || p.Selected != a.Selected || string(p.Outcomes) != string(a.Outcomes) || string(p.Cohorts) != string(a.Cohorts) ||
			string(p.Definitions) != string(a.Definitions) || string(p.Management) != string(a.Management) {
			t.Fatalf("order changed aggregates: asc=%+v desc=%+v", a, p)
		}
	}
	for _, invalid := range []string{"&order=", "&order=DESC", "&order=newest", "&order=asc&order=desc"} {
		response := rawHTTPRequest(h, base+invalid)
		var result serving.Error
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusBadRequest || result.Code != "InvalidQuery" {
			t.Fatalf("invalid order %s: status=%d body=%s", invalid, response.Code, response.Body)
		}
	}
}

func TestSchedulerHistorySummaryBoundsPayloadAndPreservesRecordIdentity(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	from := time.Now().UTC()
	to := from.Add(time.Minute)
	keepJoules, planJoules := 12.5, 7.25
	candidate := func(id string, joules *float64) serving.SchedulerCandidate {
		return serving.SchedulerCandidate{ID: id, Keep: id == "keep", ExpectedJoules: joules, Reasons: []string{strings.Repeat("large candidate evidence", 30000)}}
	}
	plan := candidate("plan", &planJoules)
	for i := range 3 {
		if err := h.observationLog.SchedulerRecord(observation.SchedulerObservation{
			At:       from.Add(time.Duration(i) * time.Second),
			Decision: &serving.SchedulerDecision{At: from, Status: "Keep", Keep: candidate("keep", &keepJoules), Target: &plan},
			Snapshot: &serving.SchedulerSnapshot{At: from, Reasons: []string{strings.Repeat("large retained evidence", 30000)}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/v1/observations/scheduler?from=" + from.Format(time.RFC3339Nano) + "&to=" + to.Format(time.RFC3339Nano)
	path := base + "&limit=2&order=desc&filter=decision-action&view=summary"
	response := rawHTTPRequest(h, path)
	var page struct {
		Events []struct {
			At       time.Time
			Offset   int
			Decision struct {
				At                                       time.Time
				Status                                   string
				KeepExpectedJoules, TargetExpectedJoules *float64
			}
			Snapshot json.RawMessage
		}
		NextCursor string
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK {
		t.Fatalf("summary response: status=%d err=%v body=%s", response.Code, err, response.Body)
	}
	if response.Body.Len() > 4096 || len(page.Events) != 2 || page.Events[0].Offset != 2 || page.Events[1].Offset != 1 || page.NextCursor != "2" {
		t.Fatalf("summary payload or pagination: bytes=%d page=%+v", response.Body.Len(), page)
	}
	if page.Events[0].Decision.Status != "Keep" || !page.Events[0].Decision.At.Equal(from) || strings.Contains(string(page.Events[0].Snapshot), "reasons") ||
		strings.Contains(response.Body.String(), "candidate evidence") || page.Events[0].Decision.KeepExpectedJoules == nil || *page.Events[0].Decision.KeepExpectedJoules != keepJoules ||
		page.Events[0].Decision.TargetExpectedJoules == nil || *page.Events[0].Decision.TargetExpectedJoules != planJoules {
		t.Fatalf("summary lost identity or included detail: %+v", page.Events[0])
	}
	detail := rawHTTPRequest(h, base+"&limit=1&cursor=2&view=compact")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "large retained evidence") {
		t.Fatalf("selected record no longer retains full detail: status=%d bytes=%d", detail.Code, detail.Body.Len())
	}
}

func TestSchedulerHistorySummaryDoesNotRestoreArchivedEvidence(t *testing.T) {
	from := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Hour)
	raw := `{"at":"` + from.Format(time.RFC3339Nano) + `","decision":{"at":"` + from.Format(time.RFC3339Nano) + `","status":"Keep","workloads":[{"uid":"w"}]},"snapshot":{"workloadIDs":["w/1"]}}` + "\n" +
		`{"at":"` + from.Add(time.Second).Format(time.RFC3339Nano) + `","node":{"uid":"node","name":"node"},"meter":"missing-meter","powerJoules":12.5,"samples":[{"watts":"detail-only"}]}` + "\n"
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := &rawHTTPArchive{body: compressed.Bytes(), objects: []evidencestore.ObjectIdentity{{
		Store: "evidence", Key: "serving/observations.v1alpha7/" + from.Format("20060102T15") + "/scheduler.jsonl.gz", Size: int64(compressed.Len()),
	}}}
	h := rawHTTPHandler(t, archive, true)
	base := "/api/v1/observations/scheduler?from=" + from.Format(time.RFC3339Nano) + "&to=" + from.Add(time.Minute).Format(time.RFC3339Nano) + "&limit=2"
	for _, selection := range []string{"", "&filter=decision-action", "&order=desc", "&order=desc&filter=decision-action&uid=w"} {
		opened := archive.opens
		response := rawHTTPRequest(h, base+"&view=summary"+selection)
		var page struct {
			Events []struct {
				Offset      *int
				PowerJoules *float64
			}
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Events) == 0 {
			t.Fatalf("summary restored archived detail: status=%d body=%s err=%v", response.Code, response.Body, err)
		}
		wantOpens := 1
		if strings.Contains(selection, "order=desc") {
			wantOpens = 2 // key-only pass, then decode only the selected summaries
		}
		if archive.opens-opened != wantOpens {
			t.Fatalf("summary archive passes=%d want=%d selection=%q", archive.opens-opened, wantOpens, selection)
		}
		for _, record := range page.Events {
			if record.Offset == nil || *record.Offset == 1 && (record.PowerJoules == nil || *record.PowerJoules != 12.5) {
				t.Fatalf("summary lost original offset or measured energy: %+v", record)
			}
		}
		if strings.Contains(response.Body.String(), "samples") || strings.Contains(response.Body.String(), "workloadIDs") {
			t.Fatalf("summary leaked archived evidence: %s", response.Body)
		}
	}
	if response := rawHTTPRequest(h, base+"&cursor=0&view=compact"); response.Code == http.StatusOK {
		t.Fatal("selected original detail silently stopped resolving its missing catalog")
	}
}
