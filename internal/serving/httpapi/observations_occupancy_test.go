package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestOccupancyObservationsBoundary(t *testing.T) {
	h := rawHTTPHandler(t, &rawHTTPArchive{}, true)
	h.mux.HandleFunc("GET "+occupancyObservationsPath, h.occupancyObservations)
	calls := 0
	h.config.OccupancyObservations = func(_ context.Context, from, to time.Time) ([]serving.OccupancyEstimate, error) {
		calls++
		if to.Sub(from) != time.Minute {
			t.Fatal("incorrect interval", from, to)
		}
		return nil, nil
	}
	for _, query := range []string{"", "?from=bad&to=bad", "?from=2026-11-03T09:00:00Z&to=2026-11-03T08:00:00Z", "?from=2026-11-03T09:00:00Z&to=2026-11-03T09:01:00Z&extra=1", "?from=2026-11-03T09:00:00Z&from=2026-11-03T09:00:00Z&to=2026-11-03T09:01:00Z"} {
		if response := rawHTTPRequest(h, occupancyObservationsPath+query); response.Code != http.StatusBadRequest {
			t.Fatalf("query %s: %d %s", query, response.Code, response.Body)
		}
	}
	if calls != 0 {
		t.Fatal("invalid query reached estimator")
	}
	path := occupancyObservationsPath + "?from=2026-11-03T09:00:00Z&to=2026-11-03T09:01:00Z"
	response := rawHTTPRequest(h, path)
	var result OccupancyObservations
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Rows == nil || result.Window.Start.Format(time.RFC3339) != "2026-11-03T09:00:00Z" || calls != 1 {
		t.Fatalf("response: %d %s calls=%d", response.Code, response.Body, calls)
	}
	unauthenticated := httptest.NewRecorder()
	h.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatal("missing bearer accepted", unauthenticated.Code)
	}
	denied := rawHTTPHandler(t, &rawHTTPArchive{}, false)
	denied.mux.HandleFunc("GET "+occupancyObservationsPath, denied.occupancyObservations)
	if response := rawHTTPRequest(denied, path); response.Code != http.StatusForbidden {
		t.Fatal("denied SAR accepted", response.Code)
	}
	h.config.OccupancyObservations = func(context.Context, time.Time, time.Time) ([]serving.OccupancyEstimate, error) {
		return nil, &serving.Error{Code: "InvalidQuery", Message: "window exceeds configured maximum"}
	}
	if response := rawHTTPRequest(h, path); response.Code != http.StatusBadRequest {
		t.Fatal("window bound error lost", response.Code)
	}
}

func TestOccupancyObservationUsesHistoricalReadDeadline(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, occupancyObservationsPath, nil)
	if !longOperation(request) {
		t.Fatal("hour-wide occupancy scan must use the historical operation deadline")
	}
	request.Method = http.MethodPost
	if longOperation(request) {
		t.Fatal("occupancy observations do not grant a long-running write route")
	}
}
