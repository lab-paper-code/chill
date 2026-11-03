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
	h.config.OccupancyObservations = func(_ context.Context, from, to time.Time) ([]serving.OccupancyEstimate, error) {
		if to.Sub(from) > 120*time.Second {
			return nil, &OccupancyWindowError{MaximumWindowSeconds: 120}
		}
		return nil, nil
	}
	expectedError := serving.Error{
		Code: "InvalidQuery", Message: "occupancy window exceeds dispatchOccupancy.aggregationWindowSeconds",
	}
	for _, end := range []string{"2026-11-03T09:02:00.000000001Z", "2026-11-03T10:00:00Z"} {
		overLimit := occupancyObservationsPath + "?from=2026-11-03T09:00:00Z&to=" + end
		response := rawHTTPRequest(h, overLimit)
		var result struct {
			serving.Error
			MaximumWindowSeconds int32 `json:"maximumWindowSeconds"`
		}
		if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
			result.Error != expectedError || result.MaximumWindowSeconds != 120 {
			t.Fatalf("oversized public window must disclose configured cap: %d %s", response.Code, response.Body)
		}
	}
	atLimit := occupancyObservationsPath + "?from=2026-11-03T09:00:00Z&to=2026-11-03T09:02:00Z"
	response = rawHTTPRequest(h, atLimit)
	var normal map[string]json.RawMessage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &normal) != nil || len(normal) != 2 ||
		normal["rows"] == nil || normal["window"] == nil {
		t.Fatalf("exact configured window response changed: %d %s", response.Code, response.Body)
	}
}

func TestOccupancyObservationUsesHistoricalReadDeadline(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, occupancyObservationsPath, nil)
	if !longOperation(request) {
		t.Fatal("bounded occupancy scan must use the historical operation deadline")
	}
	request.Method = http.MethodPost
	if longOperation(request) {
		t.Fatal("occupancy observations do not grant a long-running write route")
	}
}
