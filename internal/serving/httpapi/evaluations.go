package httpapi

import (
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lab-paper-code/chill/internal/serving"
)

type ActiveEvaluationList struct {
	Items []ActiveEvaluation `json:"items"`
}
type ActiveEvaluation struct {
	Name        string                 `json:"name"`
	UID         string                 `json:"uid"`
	StartedAt   *metav1.Time           `json:"startedAt,omitempty"`
	StageIndex  int                    `json:"stageIndex"`
	StagesTotal int                    `json:"stagesTotal"`
	Stage       *ActiveEvaluationStage `json:"stage,omitempty"`
}
type ActiveEvaluationStage struct {
	ID             string                      `json:"id"`
	StartedAt      *metav1.Time                `json:"startedAt,omitempty"`
	GoAt           *metav1.Time                `json:"goAt,omitempty"`
	PlannedSeconds float64                     `json:"plannedSeconds"`
	Injections     []ActiveEvaluationInjection `json:"injections"`
}
type ActiveEvaluationInjection struct {
	ID            string  `json:"id"`
	Action        string  `json:"action"`
	OffsetSeconds float64 `json:"offsetSeconds"`
	Outcome       string  `json:"outcome,omitempty"`
	NodeName      string  `json:"nodeName,omitempty"`
	PodName       string  `json:"podName,omitempty"`
}

func (h *Handler) activeEvaluations(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.ListActiveEvaluations == nil {
		sendError(w, &serving.Error{Code: "ServiceUnavailable", Message: "active evaluation service is unavailable"})
		return
	}
	value, err := h.config.ListActiveEvaluations(r.Context())
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, value)
}
