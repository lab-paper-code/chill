package entrypoint

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
	Persisted     bool            `json:"persisted"`
	Loaded        bool            `json:"loaded"`
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
		view, err := h.config.ProfileSelections(r.Context())
		if err != nil {
			sendError(w, err)
			return
		}
		sendJSON(w, http.StatusOK, view)
		return
	}
	principal := servingkube.PrincipalFromContext(r.Context())
	if err := h.authorizer.AuthorizeConfigMapUpdate(r.Context(), principal, h.config.SchedulerNamespace, h.config.SchedulerConfigMap); err != nil {
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
