package httpapi

import (
	"net/http"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

// ProfilesView is the catalog the scheduler derived from completed measurements:
// the latest profile per condition, its preparation and per-source issues.
type ProfilesView struct {
	ConfigVersion string `json:"configVersion"`
	servingkube.CatalogView
	Preparations map[string]servingkube.PrepareSpec `json:"preparations"`
}

func (h *Handler) schedulerProfiles(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.Profiles == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler is not enabled"})
		return
	}
	view, err := h.config.Profiles(r.Context())
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, view)
}

// schedulerCapacity returns the capacity rows the autonomous Workloads need;
// a row still being computed is CapacityPending.
func (h *Handler) schedulerCapacity(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.Scheduler == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler is not enabled"})
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Rows []serving.CapacityRow `json:"rows"`
	}{h.config.Scheduler.Capacity()})
}
