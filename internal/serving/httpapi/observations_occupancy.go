package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

const occupancyObservationsPath = "/api/v1/observations/occupancy"

type OccupancyObservations struct {
	Window serving.OccupancyWindow     `json:"window"`
	Rows   []serving.OccupancyEstimate `json:"rows"`
}

// OccupancyObservationReader applies the accepted configuration's window bound
// and estimates all cells from one ScanOccupancyRows invocation.
type OccupancyObservationReader func(context.Context, time.Time, time.Time) ([]serving.OccupancyEstimate, error)

// OccupancyWindowError reports the accepted policy's query limit.
type OccupancyWindowError struct {
	MaximumWindowSeconds int32
}

func (*OccupancyWindowError) Error() string {
	return "occupancy window exceeds dispatchOccupancy.aggregationWindowSeconds"
}

func (h *Handler) occupancyObservations(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || r.URL.RawPath != "" || len(query) != 2 || len(query["from"]) != 1 || len(query["to"]) != 1 {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "exactly one RFC3339 from and to are required"})
		return
	}
	from, fromErr := time.Parse(time.RFC3339Nano, query.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, query.Get("to"))
	if fromErr != nil || toErr != nil || !from.Before(to) {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "from must precede to; both must be RFC3339 timestamps"})
		return
	}
	if h.config.OccupancyObservations == nil {
		sendError(w, &serving.Error{Code: "ObservationUnavailable", Message: "occupancy observation policy is unavailable"})
		return
	}
	rows, err := h.config.OccupancyObservations(r.Context(), from, to)
	if err != nil {
		var limit *OccupancyWindowError
		if errors.As(err, &limit) {
			sendJSON(w, http.StatusBadRequest, struct {
				serving.Error
				MaximumWindowSeconds int32 `json:"maximumWindowSeconds"`
			}{
				Error:                serving.Error{Code: "InvalidQuery", Message: limit.Error()},
				MaximumWindowSeconds: limit.MaximumWindowSeconds,
			})
			return
		}
		sendError(w, err)
		return
	}
	if rows == nil {
		rows = []serving.OccupancyEstimate{}
	}
	w.Header().Set("Cache-Control", "no-store")
	sendJSON(w, http.StatusOK, OccupancyObservations{Window: serving.OccupancyWindow{Start: from.UTC(), End: to.UTC()}, Rows: rows})
}
