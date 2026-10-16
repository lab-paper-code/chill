package httpapi

import (
	"net/http"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

type ProfileCampaign struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}
type ProfileSelectionRequest struct {
	Campaign ProfileCampaign `json:"campaign"`
}
type ProfileSelectionResult struct {
	Campaign      ProfileCampaign `json:"campaign"`
	Status        string          `json:"status"`
	ProfileID     string          `json:"profileID,omitempty"`
	Code          string          `json:"code,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	ConfigVersion string          `json:"configVersion,omitempty"`
	// Persisted means the campaign remains explicitly selected, not that the
	// latest operation was persisted. Removal returns false after its CM commit.
	Persisted bool `json:"persisted"`
	// For removal, Loaded tracks retained accepted catalog publication, not
	// physical instance readiness. Status is Removed only after ordinary refresh
	// succeeds; failed post-commit refresh is Pending with ProfileUnloadFailed.
	Loaded bool `json:"loaded"`
}
type ProfileSelectionsView struct {
	ConfigVersion string                               `json:"configVersion"`
	Selections    []ProfileSelectionResult             `json:"selections"`
	State         servingkube.SchedulerControllerState `json:"state"`
}

func (h *Handler) schedulerProfiles(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.ProfileSelections == nil || h.config.SelectProfile == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler profile selection is not enabled"})
		return
	}
	if r.Method == http.MethodGet {
		compact, err := schedulerCompactQuery(r)
		if err != nil {
			sendError(w, err)
			return
		}
		view, err := h.config.ProfileSelections(r.Context())
		if err != nil {
			sendError(w, err)
			return
		}
		if compact {
			evidence := newSchedulerReadEvidence()
			state := evidence.state(&view.State)
			sendJSON(w, http.StatusOK, struct {
				*ProfileSelectionsView
				State schedulerReadState `json:"state"`
				schedulerReadEvidence
			}{&view, state, evidence})
			return
		}
		sendJSON(w, http.StatusOK, view)
		return
	}
	principal := servingkube.PrincipalFromContext(r.Context())
	if err := h.authorizer.AuthorizeConfigMapUpdate(r.Context(), principal,
		h.config.SchedulerNamespace, h.config.SchedulerConfigMap); err != nil {
		sendError(w, err)
		return
	}
	var request ProfileSelectionRequest
	if err := h.decode(w, r, &request); err != nil {
		sendError(w, err)
		return
	}
	result, err := h.config.SelectProfile(r.Context(), request, principal)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, result)
}

func (h *Handler) removeSchedulerProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.RemoveProfile == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler profile removal is not enabled"})
		return
	}
	principal := servingkube.PrincipalFromContext(r.Context())
	if err := h.authorizer.AuthorizeConfigMapUpdate(r.Context(), principal,
		h.config.SchedulerNamespace, h.config.SchedulerConfigMap); err != nil {
		sendError(w, err)
		return
	}
	version, err := precondition(r)
	if err != nil {
		sendError(w, err)
		return
	}
	result, err := h.config.RemoveProfile(r.Context(), ProfileCampaign{
		Namespace: r.PathValue("namespace"), Name: r.PathValue("name"),
	}, version, principal)
	if err != nil {
		sendError(w, err)
		return
	}
	status := http.StatusOK
	if result.Code == "ProfileInUse" || result.Code == "ProfileControlsEnabled" {
		status = http.StatusConflict
	}
	sendJSON(w, status, result)
}
