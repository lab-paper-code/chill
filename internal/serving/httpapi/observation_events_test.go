package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func TestObservationEventsRecoverSimultaneousCommittedFacts(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	h.mux.HandleFunc("GET /api/v1/observations/events", h.observationEvents)
	at := time.Now().UTC()
	seconds := 15.0
	action := &observation.SchedulerActionObservation{ID: "operation", Kind: "power", Node: serving.NodeReference{Name: "edge", UID: "node-uid"}, Phase: "failed", StartedAt: at.Add(-time.Minute), CompletedAt: &at, Error: "power-mode-timeout", ExpectedSeconds: &seconds}
	if err := h.observationLog.SchedulerRecord(observation.SchedulerObservation{At: at, Action: action}); err != nil {
		t.Fatal(err)
	}
	cluster := observation.ClusterObservation{Kind: "issue", At: at, Event: "raised", Source: "pod-watch", Reason: "not-serving", Pod: &observation.ClusterPod{UID: "pod-uid", Name: "worker", Node: "edge"}}
	if err := h.observationLog.ClusterRecord(cluster); err != nil {
		t.Fatal(err)
	}
	// Unrelated snapshot payload must not be resolved through its catalog.
	if err := h.observationLog.SchedulerRecord(observation.SchedulerObservation{At: at.Add(time.Nanosecond), Snapshot: &serving.SchedulerSnapshot{At: at}}); err != nil {
		t.Fatal(err)
	}
	response := rawHTTPRequest(h, "/api/v1/observations/events?"+url.Values{"at": {at.Format(time.RFC3339Nano)}}.Encode())
	var page struct {
		Events []struct {
			Type string    `json:"type"`
			At   time.Time `json:"at"`
			live.Event
		} `json:"events"`
		Coverage serving.ObservationCoverage `json:"coverage"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Events) != 2 {
		t.Fatalf("exact committed recovery: status=%d body=%s err=%v", response.Code, response.Body, err)
	}
	if page.Coverage.From == nil || !page.Coverage.From.Equal(at.Truncate(time.Hour)) || page.Coverage.To == nil || page.Coverage.To.Before(at) {
		t.Fatalf("found event has no append coverage: %+v", page.Coverage)
	}
	for _, event := range page.Events {
		if event.Type != "event" || !event.At.Equal(at) {
			t.Fatalf("event identity changed: %+v", event)
		}
		if event.Action != nil && (event.Action.Result == nil || event.Action.Result.Error != action.Error || event.Action.ExpectedSeconds == nil || *event.Action.ExpectedSeconds != seconds) {
			t.Fatalf("action failure/expectation lost: %+v", event.Action)
		}
		if event.Cluster != nil && (event.Cluster.Reason != cluster.Reason || event.Cluster.Pod.UID != cluster.Pod.UID) {
			t.Fatalf("cluster identity lost: %+v", event.Cluster)
		}
	}
	for _, query := range []string{"", "?at=bad", "?at=2026-01-01T00:00:00Z&at=2026-01-01T00:00:00Z", "?at=2026-01-01T00:00:00Z&limit=1", "?at=2026-01-01T00:00:00Z&cursor=bad", "?at=2026-01-01T00:00:00Z&cursor=e30", "?at=2026-01-01T00:00:00Z&cursor=2026-01-01T00:01:00Z"} {
		if response := rawHTTPRequest(h, "/api/v1/observations/events"+query); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid exact query accepted: %s -> %d", query, response.Code)
		}
	}
	denied := rawHTTPHandler(t, &rawHTTPArchive{}, false)
	denied.mux.HandleFunc("GET /api/v1/observations/events", denied.observationEvents)
	if response := rawHTTPRequest(denied, "/api/v1/observations/events?at="+url.QueryEscape(at.Format(time.RFC3339Nano))); response.Code != http.StatusForbidden {
		t.Fatalf("event read bypassed authorization: %d", response.Code)
	}
	if events, next, _, err := h.observationLog.EventObservations(context.Background(), at, 1, ""); err != nil || len(events) != 1 || next == "" {
		t.Fatalf("event bound lost continuation for simultaneous facts: %+v next=%q err=%v", events, next, err)
	}
}

func TestObservationEventRangeSortsWithinAppendPage(t *testing.T) {
	from := time.Date(2026, 11, 5, 2, 59, 30, 0, time.UTC)
	to := from.Add(time.Minute)
	now := from
	h := rawHTTPHandlerWithClock(t, &rawHTTPArchive{}, true, func() time.Time { return now })
	h.mux.HandleFunc("GET /api/v1/observations/events", h.observationEvents)
	// Commit in a different order from event time, including identical times.
	for _, row := range []observation.ClusterObservation{
		{Kind: "issue", At: from.Add(time.Second), Event: "raised", Source: "second", Reason: "fact"},
		{Kind: "issue", At: from, Event: "raised", Source: "first", Reason: "fact"},
		{Kind: "issue", At: to, Event: "raised", Source: "excluded", Reason: "fact"},
	} {
		if err := h.observationLog.ClusterRecord(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.observationLog.SchedulerRecord(observation.SchedulerObservation{At: from, Action: &observation.SchedulerActionObservation{ID: "power-operation", Kind: "power", StartedAt: from}}); err != nil {
		t.Fatal(err)
	}
	now = to
	query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}, "limit": {"3"}}
	response := rawHTTPRequest(h, "/api/v1/observations/events?"+query.Encode())
	var page struct {
		Events     []observationEvent `json:"events"`
		NextCursor string             `json:"nextCursor"`
		Coverage   *serving.ObservationCoverage
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Events) != 3 || page.Coverage == nil {
		t.Fatalf("range page: %d %s %v", response.Code, response.Body, err)
	}
	if page.Events[0].Cluster == nil || page.Events[0].Cluster.Source != "first" ||
		page.Events[1].Action == nil || page.Events[1].Action.ID != "power-operation" ||
		page.Events[2].Cluster == nil || page.Events[2].Cluster.Source != "second" {
		t.Fatalf("events omitted or not event-time sorted within page: %+v", page.Events)
	}
	query.Set("cursor", "e30")
	if response := rawHTTPRequest(h, "/api/v1/observations/events?"+query.Encode()); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid range cursor accepted: %d %s", response.Code, response.Body)
	}
}

func TestObservationEventRangeQueryBounds(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	h.mux.HandleFunc("GET /api/v1/observations/events", h.observationEvents)
	valid := url.Values{"from": {"2026-10-01T00:00:00Z"}, "to": {"2026-10-02T00:00:00Z"}}
	for _, mutation := range []url.Values{
		{"from": {"bad"}}, {"from": {""}}, {"from": {"2026-10-02T00:00:00Z"}},
		{"to": {"2026-10-02T00:00:00.000000001Z"}}, {"to": {""}},
		{"limit": {"0"}}, {"limit": {"501"}}, {"limit": {"bad"}}, {"limit": {""}},
		{"cursor": {"garbage"}}, {"cursor": {""}}, {"unknown": {"1"}},
		{"at": {"2026-10-01T00:00:00Z"}}, {"from": {"2026-10-01T00:00:00Z", "2026-10-01T00:00:00Z"}},
	} {
		query := url.Values{"from": valid["from"], "to": valid["to"]}
		for key, values := range mutation {
			query[key] = values
		}
		if response := rawHTTPRequest(h, "/api/v1/observations/events?"+query.Encode()); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid range accepted %v: %d %s", query, response.Code, response.Body)
		}
	}
	for _, limit := range []string{"", "1", strconv.Itoa(min(500, h.config.MaxQueryEvents))} {
		query := url.Values{"from": valid["from"], "to": valid["to"]}
		if limit != "" {
			query.Set("limit", limit)
		}
		if response := rawHTTPRequest(h, "/api/v1/observations/events?"+query.Encode()); response.Code != http.StatusOK {
			t.Fatalf("valid bounded range rejected %v: %d %s", query, response.Code, response.Body)
		}
	}
}

func TestObservationEventsEmptyPageContinuesAppendHours(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	h.mux.HandleFunc("GET /api/v1/observations/events", h.observationEvents)
	at := time.Now().UTC().Truncate(time.Hour).Add(-2*time.Hour + time.Second)
	query := url.Values{"at": {at.Format(time.RFC3339Nano)}}
	for page := range 2 {
		response := rawHTTPRequest(h, "/api/v1/observations/events?"+query.Encode())
		var result struct {
			Events   json.RawMessage             `json:"events"`
			Next     string                      `json:"nextCursor"`
			Coverage serving.ObservationCoverage `json:"coverage"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
			t.Fatalf("empty exact page: status=%d body=%s err=%v", response.Code, response.Body, err)
		}
		from := at.Truncate(time.Hour).Add(time.Duration(page) * time.Hour)
		to := from.Add(time.Hour)
		if string(result.Events) != "[]" || result.Next != to.Format(time.RFC3339Nano) || result.Coverage.From == nil || !result.Coverage.From.Equal(from) || result.Coverage.To == nil || !result.Coverage.To.Equal(to) || len(result.Coverage.Gaps) != 2 {
			t.Fatalf("empty exact continuation contract: %s", response.Body)
		}
		query.Set("cursor", result.Next)
	}
	// Exact and range queries share append-hour cursor rules.
	rangeQuery := url.Values{"from": {at.Format(time.RFC3339Nano)}, "to": {at.Add(time.Minute).Format(time.RFC3339Nano)}, "cursor": {query.Get("cursor")}}
	if response := rawHTTPRequest(h, "/api/v1/observations/events?"+rangeQuery.Encode()); response.Code != http.StatusOK {
		t.Fatalf("exact append cursor rejected by range query: %d %s", response.Code, response.Body)
	}
}
