package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

const manualOperationsPath = "/api/v1/scheduler/operations"

// longOperation bounds operations and historical observation I/O by OperationTimeout.
func longOperation(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == manualOperationsPath ||
		r.Method == http.MethodGet && (r.URL.Path == "/api/v1/observations/scheduler" ||
			r.URL.Path == rawObservationsPath || strings.HasPrefix(r.URL.Path, rawObservationsPath+"/"))
}

// operations runs a manual operation for a caller allowed to change the
// scheduler configuration.
func (h *Handler) operations(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.Scheduler == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler configuration is not enabled"})
		return
	}
	principal := servingkube.PrincipalFromContext(r.Context())
	if err := h.authorizer.AuthorizeConfigMapUpdate(r.Context(), principal,
		h.config.SchedulerNamespace, h.config.SchedulerConfigMap); err != nil {
		sendError(w, err)
		return
	}
	var raw json.RawMessage
	if err := h.decode(w, r, &raw); err != nil {
		sendError(w, err)
		return
	}
	request, err := servingkube.DecodeManualOperation(raw)
	if err != nil {
		sendError(w, err)
		return
	}
	request.ActorUsername, request.ActorUID = principal.Actor()
	result, err := h.config.Scheduler.ExecuteManual(r.Context(), request)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, result)
}
