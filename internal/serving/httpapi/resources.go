package httpapi

import (
	"net/http"

	"github.com/lab-paper-code/chill/internal/serving"
)

func (h *Handler) resources(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	var batch serving.ResourceBatch
	if err := h.decode(w, r, &batch); err != nil {
		sendError(w, err)
		return
	}
	if h.config.ReceiveResources == nil {
		sendError(w, &serving.Error{Code: "ObservationUnavailable", Message: "resource receiver is not configured"})
		return
	}
	targets, err := h.config.ReceiveResources(r.Context(), batch)
	if err != nil {
		sendError(w, err)
		return
	}
	if targets == nil {
		targets = []serving.ResourceTarget{}
	}
	sendJSON(w, http.StatusOK, struct {
		Targets []serving.ResourceTarget `json:"targets"`
	}{targets})
}
