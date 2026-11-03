package httpapi

import (
	"net/http"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

const powerColumnsFormat = "columns"

func (h *Handler) operatingPower(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if len(values) != 1 || (key != "from" && key != "to" && key != "bucket" && key != "format") {
			sendError(w, &serving.Error{Code: "InvalidQuery", Message: "unknown or repeated power query"})
			return
		}
	}
	if query.Has("format") && (query.Get("format") != powerColumnsFormat || !query.Has("bucket")) {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "format=columns requires bucket"})
		return
	}
	from, fromErr := time.Parse(time.RFC3339Nano, query.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, query.Get("to"))
	if fromErr != nil || toErr != nil || from.IsZero() || !from.Before(to) {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "explicit positive RFC3339 from/to power interval is required"})
		return
	}
	if query.Has("bucket") {
		bucket, err := time.ParseDuration(query.Get("bucket"))
		if err != nil {
			sendError(w, &serving.Error{Code: "InvalidQuery", Message: "bucket must be a Go duration between 1s and 1h"})
			return
		}
		if _, err := observation.PowerBucketCount(from, to, bucket); err != nil {
			sendError(w, err)
			return
		}
		if h.config.OperatingPowerSeries == nil {
			sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler meter configuration is not enabled"})
			return
		}
		result, err := h.config.OperatingPowerSeries(r.Context(), from, to, bucket, h.config.MaxQueryEvents)
		if err != nil {
			sendError(w, err)
			return
		}
		if query.Get("format") == powerColumnsFormat {
			sendJSON(w, http.StatusOK, result.Columns())
			return
		}
		sendJSON(w, http.StatusOK, result)
		return
	}
	if h.config.OperatingPower == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler meter configuration is not enabled"})
		return
	}
	result, err := h.config.OperatingPower(r.Context(), from, to, h.config.MaxQueryEvents)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, result)
}
