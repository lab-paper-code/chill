package httpapi

import (
	"net/http"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func (h *Handler) operatingPower(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	query := r.URL.Query()
	from, fromErr := time.Parse(time.RFC3339Nano, query.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, query.Get("to"))
	if fromErr != nil || toErr != nil || from.IsZero() || !from.Before(to) {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "explicit positive RFC3339 from/to power interval is required"})
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
