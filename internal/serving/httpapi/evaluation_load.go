package httpapi

import (
	"net/http"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type LoadScenarioRequest struct {
	Name string               `json:"name"`
	Spec api.LoadScenarioSpec `json:"spec"`
}
type LoadScenarioView struct {
	Name      string               `json:"name"`
	UID       string               `json:"uid"`
	Spec      api.LoadScenarioSpec `json:"spec"`
	InUse     []string             `json:"inUse"`
	CreatedAt metav1.Time          `json:"createdAt"`
}
type LoadScenarioList struct {
	Items []LoadScenarioView `json:"items"`
}
type LiveWorkload struct {
	UID        string `json:"uid"`
	Generation int64  `json:"generation"`
}
type LoadScenarioPreflight struct {
	Checks        []EvaluationCheck       `json:"checks"`
	LiveWorkloads map[string]LiveWorkload `json:"liveWorkloads"`
}
type EvaluationInput struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	Size  int64  `json:"size"`
	Kind  string `json:"kind"`
}
type EvaluationInputList struct {
	Items []EvaluationInput `json:"items"`
}

func (h *Handler) registerEvaluationLoadRoutes() {
	for _, route := range []string{
		"GET /api/v1/evaluations/load-scenarios",
		"GET /api/v1/evaluations/load-scenarios/{name}",
		"POST /api/v1/evaluations/load-scenarios/preflight",
		"POST /api/v1/evaluations/load-scenarios",
		"PUT /api/v1/evaluations/load-scenarios/{name}",
		"DELETE /api/v1/evaluations/load-scenarios/{name}",
		"GET /api/v1/evaluations/inputs",
	} {
		h.mux.HandleFunc(route, h.evaluationLoads)
	}
}
func (h *Handler) evaluationLoads(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	var value any
	var err error
	name, uid := r.PathValue("name"), r.URL.Query().Get("uid")
	available := false
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/evaluations/inputs":
		available = h.config.ListEvaluationInputs != nil
	case r.Method == http.MethodGet && name == "":
		available = h.config.ListLoadScenarios != nil
	case r.Method == http.MethodGet:
		available = h.config.GetLoadScenario != nil
	case r.URL.Path == "/api/v1/evaluations/load-scenarios/preflight":
		available = h.config.PreflightLoadScenario != nil
	case r.Method == http.MethodPost:
		available = h.config.CreateLoadScenario != nil
	case r.Method == http.MethodPut:
		available = h.config.ReplaceLoadScenario != nil
	case r.Method == http.MethodDelete:
		available = h.config.DeleteLoadScenario != nil
	}
	if !available {
		sendError(w, &serving.Error{Code: "ServiceUnavailable", Message: "load scenario service is unavailable"})
		return
	}
	if r.Method == http.MethodGet {
		switch {
		case r.URL.Path == "/api/v1/evaluations/inputs":
			value, err = h.config.ListEvaluationInputs(r.Context())
		case name == "":
			value, err = h.config.ListLoadScenarios(r.Context())
		default:
			value, err = h.config.GetLoadScenario(r.Context(), name)
		}
	} else {
		var request LoadScenarioRequest
		if r.Method != http.MethodDelete {
			if err = h.decode(w, r, &request); err != nil {
				sendError(w, err)
				return
			}
		}
		if r.URL.Path == "/api/v1/evaluations/load-scenarios/preflight" {
			value, err = h.config.PreflightLoadScenario(r.Context(), request)
		} else {
			if name == "" {
				name = request.Name
			}
			var audit *serving.SchedulerActionObservation
			audit, err = h.beginEvaluationAudit(r, name, uid)
			if err == nil {
				actor, _ := servingkube.PrincipalFromContext(r.Context()).Actor()
				switch r.Method {
				case http.MethodPost:
					value, err = h.config.CreateLoadScenario(r.Context(), request, actor)
				case http.MethodPut:
					value, err = h.config.ReplaceLoadScenario(r.Context(), name, uid, request, actor)
				case http.MethodDelete:
					value, err = h.config.DeleteLoadScenario(r.Context(), name, uid)
				}
				err = h.finishEvaluationAudit(audit, value, err)
			}
		}
	}
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, value)
}
