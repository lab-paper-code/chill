package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

// NewRouterHandler reuses the inference boundary without constructing a manager,
// observer, scheduler, or any management/query routes.
func NewRouterHandler(runtime *serving.Runtime, authorizer *servingkube.Authorizer, log *serving.ObservationLog, config HandlerConfig) (http.Handler, error) {
	if runtime == nil || authorizer == nil || authorizer.Client == nil || log == nil || config.MaxRequestBytes <= 0 || config.RequestTimeout <= 0 {
		return nil, errors.New("router runtime, Kubernetes reviews, observations and positive HTTP bounds required")
	}
	h := &Handler{runtime: runtime, authorizer: authorizer, observationLog: log, config: config, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, http.StatusOK, struct {
			Alive bool `json:"alive"`
		}{true})
	})
	h.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ready := runtime.GatewayReady() && log.Health().Healthy
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		sendJSON(w, status, struct {
			Ready bool `json:"ready"`
		}{ready})
	})
	h.mux.HandleFunc("POST /api/v1/workloads/{id}/infer", h.infer)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject aliases, encoded separators and ServeMux cleaning redirects. Existing
		// keep-alive clients must cross the local stale gate on every infer call.
		path := r.URL.EscapedPath()
		exactHealth := (path == "/healthz" || path == "/readyz") && r.Method == http.MethodGet
		segments := strings.Split(path, "/")
		exactInfer := len(segments) == 6 && segments[1] == "api" && segments[2] == "v1" && segments[3] == "workloads" && segments[4] != "" && !strings.Contains(segments[4], "%") && segments[4] != "." && segments[4] != ".." && segments[5] == "infer" && r.Method == http.MethodPost
		if (!exactHealth && !exactInfer) || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}), nil
}
