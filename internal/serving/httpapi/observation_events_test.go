package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

func TestObservationEventsRecoverSimultaneousCommittedFacts(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	h.mux.HandleFunc("GET /api/v1/observations/events", h.observationEvents)
	at := time.Now().UTC()
	seconds := 15.0
	action := &serving.SchedulerActionObservation{ID: "operation", Kind: "power", Node: serving.NodeReference{Name: "edge", UID: "node-uid"}, Phase: "failed", StartedAt: at.Add(-time.Minute), CompletedAt: &at, Error: "power-mode-timeout", ExpectedSeconds: &seconds}
	if err := h.observationLog.SchedulerRecord(serving.SchedulerObservation{At: at, Action: action}); err != nil {
		t.Fatal(err)
	}
	cluster := serving.ClusterObservation{Kind: "issue", At: at, Event: "raised", Source: "pod-watch", Reason: "not-serving", Pod: &serving.ClusterPod{UID: "pod-uid", Name: "worker", Node: "edge"}}
	if err := h.observationLog.ClusterRecord(cluster); err != nil {
		t.Fatal(err)
	}
	// Unrelated snapshot payload must not be resolved through its catalog.
	if err := h.observationLog.SchedulerRecord(serving.SchedulerObservation{At: at.Add(time.Nanosecond), Snapshot: &serving.SchedulerSnapshot{At: at}}); err != nil {
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
