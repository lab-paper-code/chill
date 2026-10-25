package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

const manualOperationsPath = "/api/v1/scheduler/operations"

// longOperation reports a route that runs a physical operation to its end and
// is bounded by OperationTimeout.
func longOperation(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == manualOperationsPath
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
