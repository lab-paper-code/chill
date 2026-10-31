package httpapi

import (
	"net/http"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func (h *Handler) evaluationPreflight(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	var request EvaluationRequest
	if err := h.decode(w, r, &request); err != nil {
		sendError(w, err)
		return
	}
	value, err := h.config.PreflightEvaluation(r.Context(), request)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, value)
}
func (h *Handler) evaluations(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	name := r.PathValue("name")
	if r.Method == http.MethodGet {
		var value any
		var err error
		if name == "" {
			value, err = h.config.ListEvaluations(r.Context())
		} else {
			value, err = h.config.GetEvaluation(r.Context(), name)
		}
		if err != nil {
			sendError(w, err)
			return
		}
		sendJSON(w, http.StatusOK, value)
		return
	}
	uid := r.URL.Query().Get("uid")
	isRelease := r.Method == http.MethodPost && name != ""
	var request EvaluationRequest
	var release EvaluationReleaseRequest
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		if isRelease {
			if err := h.decode(w, r, &release); err != nil {
				sendError(w, err)
				return
			}
			uid = release.UID
		} else {
			if err := h.decode(w, r, &request); err != nil {
				sendError(w, err)
				return
			}
			if name == "" {
				name = request.Name
			}
		}
	}
	if r.Method != http.MethodPost && uid == "" {
		sendError(w, &serving.Error{Code: "InvalidInput", Message: "uid is required", Field: "uid"})
		return
	}
	audit, err := h.beginEvaluationAudit(r, name, uid)
	if err != nil {
		sendError(w, err)
		return
	}
	actor, _ := servingkube.PrincipalFromContext(r.Context()).Actor()
	var value EvaluationIdentity
	switch {
	case isRelease:
		value, err = h.config.ReleaseEvaluation(r.Context(), name, release, actor)
	case r.Method == http.MethodPost:
		value, err = h.config.CreateEvaluation(r.Context(), request, actor)
	case r.Method == http.MethodPut:
		value, err = h.config.ReplaceEvaluation(r.Context(), name, uid, request, actor)
	case r.Method == http.MethodDelete:
		value, err = h.config.DeleteEvaluation(r.Context(), name, uid, actor)
	}
	if err = h.finishEvaluationAudit(audit, value, err); err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusAccepted, value)
}
