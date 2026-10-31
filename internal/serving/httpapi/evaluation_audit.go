package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func (h *Handler) beginEvaluationAudit(r *http.Request, name, uid string) (*serving.SchedulerActionObservation, error) {
	if h.observationLog == nil {
		return nil, &serving.Error{Code: "EvaluationAuditUnavailable", Message: "durable evaluation audit is unavailable"}
	}
	username, actorUID := servingkube.PrincipalFromContext(r.Context()).Actor()
	now := time.Now().UTC()
	expected, err := json.Marshal(struct {
		ActorUsername string `json:"actorUsername"`
		ActorUID      string `json:"actorUID"`
		Name          string `json:"name"`
		UID           string `json:"uid,omitempty"`
		Operation     string `json:"operation"`
	}{username, actorUID, name, uid, r.Method + " " + r.URL.Path})
	if err != nil {
		return nil, err
	}
	action := &serving.SchedulerActionObservation{
		ID:   "evaluation/" + r.Method + "/" + name + "/" + now.Format(time.RFC3339Nano),
		Kind: "evaluation-" + strings.ToLower(r.Method), Phase: "validated", StartedAt: now, Expected: expected,
	}
	if err := h.recordMutationAudit(action); err != nil {
		return nil, err
	}
	return action, nil
}
func (h *Handler) finishEvaluationAudit(
	action *serving.SchedulerActionObservation, value any, mutationErr error,
) error {
	now := time.Now().UTC()
	action.CompletedAt = &now
	action.Phase = "accepted"
	if mutationErr != nil {
		action.Phase = "failed"
		action.Error = errorCode(mutationErr)
	}
	action.Actual, _ = json.Marshal(value)
	recordErr := h.recordMutationAudit(action)
	if recordErr != nil && mutationErr == nil {
		return &serving.Error{
			Code:    "EvaluationAuditIncomplete",
			Message: "evaluation mutation may have been accepted but audit failed; query current objects before retrying",
		}
	}
	return mutationErr
}
