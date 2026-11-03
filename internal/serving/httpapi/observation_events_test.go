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
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Events) != 2 {
		t.Fatalf("exact committed recovery: status=%d body=%s err=%v", response.Code, response.Body, err)
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
	for _, query := range []string{"", "?at=bad", "?at=2026-01-01T00:00:00Z&at=2026-01-01T00:00:00Z", "?at=2026-01-01T00:00:00Z&limit=1"} {
		if response := rawHTTPRequest(h, "/api/v1/observations/events"+query); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid exact query accepted: %s -> %d", query, response.Code)
		}
	}
	denied := rawHTTPHandler(t, &rawHTTPArchive{}, false)
	denied.mux.HandleFunc("GET /api/v1/observations/events", denied.observationEvents)
	if response := rawHTTPRequest(denied, "/api/v1/observations/events?at="+url.QueryEscape(at.Format(time.RFC3339Nano))); response.Code != http.StatusForbidden {
		t.Fatalf("event read bypassed authorization: %d", response.Code)
	}
	if _, err := h.observationLog.EventObservations(context.Background(), at, 1); err == nil {
		t.Fatal("event bound silently dropped simultaneous facts")
	}
}

func TestObservationEventRangePagesAreStableCommittedPrefixes(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	h.mux.HandleFunc("GET /api/v1/observations/events", h.observationEvents)
	from := time.Now().UTC().Add(-time.Minute)
	to := from.Add(time.Minute)
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
	query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}, "limit": {"1"}}
	var got []string
	for pageNumber := range 3 {
		response := rawHTTPRequest(h, "/api/v1/observations/events?"+query.Encode())
		var page struct {
			Events     []observationEvent `json:"events"`
			NextCursor string             `json:"nextCursor"`
			Coverage   *serving.ObservationCoverage
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Events) != 1 || page.Coverage == nil {
			t.Fatalf("range page: %d %s %v", response.Code, response.Body, err)
		}
		event := page.Events[0]
		if event.Cluster != nil {
			got = append(got, event.Cluster.Source)
		} else {
			got = append(got, event.Action.ID)
		}
		if pageNumber == 0 {
			// Later appends cannot move cursor positions or enter this read.
			if err := h.observationLog.ClusterRecord(observation.ClusterObservation{Kind: "issue", At: from.Add(500 * time.Millisecond), Event: "raised", Source: "later-commit", Reason: "fact"}); err != nil {
				t.Fatal(err)
			}
		}
		if (pageNumber < 2) != (page.NextCursor != "") {
			t.Fatalf("page boundary %d: %s", pageNumber, response.Body)
		}
		query.Set("cursor", page.NextCursor)
	}
	if len(got) != 3 || got[0] != "first" || got[1] != "power-operation" || got[2] != "second" {
		t.Fatalf("events duplicated, omitted or reordered: %v", got)
	}
	query.Del("cursor")
	query.Set("limit", "2")
	response := rawHTTPRequest(h, "/api/v1/observations/events?"+query.Encode())
	var page struct{ NextCursor string }
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	query.Set("cursor", page.NextCursor)
	query.Set("to", to.Add(time.Second).Format(time.RFC3339Nano))
	if response := rawHTTPRequest(h, "/api/v1/observations/events?"+query.Encode()); response.Code != http.StatusBadRequest {
		t.Fatalf("cursor reused with another interval: %d", response.Code)
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
