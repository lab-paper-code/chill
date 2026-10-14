// Package entrypoint owns the authenticated Workload HTTP boundary.
package entrypoint

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
)

const requestTimeoutCode = "RequestTimeout"

type HandlerConfig struct {
	MaxRequestBytes                                            int64
	RequestTimeout                                             time.Duration
	MaxQueryEvents                                             int
	ObservationWindow                                          time.Duration
	Scheduler                                                  *servingkube.SchedulerController
	SchedulerConfigMap, SchedulerConfigKey, SchedulerNamespace string
	SchedulerLock                                              *sync.Mutex
	OperatingPower                                             func(time.Time, time.Time, int) (serving.OperatingPowerWindow, error)
}
type Handler struct {
	manager    *servingkube.Manager
	runtime    *serving.Runtime
	authorizer *servingkube.Authorizer
	journal    *serving.Journal
	observer   *servingkube.Observer
	config     HandlerConfig
	mux        *http.ServeMux
}

func NewHandler(manager *servingkube.Manager, runtime *serving.Runtime, authorizer *servingkube.Authorizer, journal *serving.Journal, observer *servingkube.Observer, config HandlerConfig) (*Handler, error) {
	if manager == nil || runtime == nil || authorizer == nil || journal == nil || observer == nil || config.MaxRequestBytes <= 0 || config.RequestTimeout <= 0 || config.MaxQueryEvents <= 0 {
		return nil, errors.New("concrete management/runtime/auth/observation dependencies and positive HTTP bounds are required")
	}
	h := &Handler{manager: manager, runtime: runtime, authorizer: authorizer, journal: journal, observer: observer, config: config, mux: http.NewServeMux()}
	if h.config.SchedulerLock == nil {
		h.config.SchedulerLock = &sync.Mutex{}
	}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, http.StatusOK, struct {
			Alive bool `json:"alive"`
		}{true})
	})
	h.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		healthy := journal.Health().Healthy
		status := http.StatusOK
		if !healthy {
			status = http.StatusServiceUnavailable
		}
		sendJSON(w, status, struct {
			Ready bool `json:"ready"`
		}{healthy})
	})
	h.mux.HandleFunc("GET /api/v1/workloads", h.management)
	h.mux.HandleFunc("POST /api/v1/workloads", h.management)
	h.mux.HandleFunc("GET /api/v1/workloads/{id}", h.management)
	h.mux.HandleFunc("PUT /api/v1/workloads/{id}", h.management)
	h.mux.HandleFunc("DELETE /api/v1/workloads/{id}", h.management)
	h.mux.HandleFunc("POST /api/v1/workloads/{id}/infer", h.infer)
	h.mux.HandleFunc("GET /api/v1/models", h.management)
	h.mux.HandleFunc("POST /api/v1/models", h.management)
	h.mux.HandleFunc("GET /api/v1/models/{id}", h.management)
	h.mux.HandleFunc("PUT /api/v1/models/{id}", h.management)
	h.mux.HandleFunc("DELETE /api/v1/models/{id}", h.management)
	h.mux.HandleFunc("GET /api/v1/pools", h.management)
	h.mux.HandleFunc("GET /api/v1/pools/{id}", h.management)
	h.mux.HandleFunc("GET /api/v1/observations/workloads/{uid}", h.observations)
	h.mux.HandleFunc("GET /api/v1/scheduler", h.scheduler)
	h.mux.HandleFunc("GET /api/v1/scheduler/settings", h.scheduler)
	h.mux.HandleFunc("GET /api/v1/scheduler/state", h.scheduler)
	h.mux.HandleFunc("PUT /api/v1/scheduler/settings", h.scheduler)
	h.mux.HandleFunc("PUT /api/v1/scheduler", h.scheduler)
	h.mux.HandleFunc("GET /api/v1/observations/load", h.schedulerObservations)
	h.mux.HandleFunc("GET /api/v1/observations/scheduler", h.schedulerObservations)
	h.mux.HandleFunc("GET /api/v1/observations/power", h.operatingPower)
	return h, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.config.RequestTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		sendError(w, err)
		return
	}
	// Keep a separate bounded delivery phase after the operation times out.
	writeDeadline := deadline.Add(h.config.RequestTimeout)
	if err := controller.SetWriteDeadline(writeDeadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		sendError(w, err)
		return
	}
	h.mux.ServeHTTP(w, r.WithContext(ctx))
}

func (h *Handler) authorize(r *http.Request) error {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return servingkube.ErrUnauthenticated
	}
	principal, err := h.authorizer.Authorize(r.Context(), strings.TrimSpace(token), r.Method, r.URL.Path)
	if err == nil {
		*r = *r.WithContext(servingkube.WithPrincipal(r.Context(), principal))
	}
	return err
}
func (h *Handler) decode(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, h.config.MaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return &serving.Error{Code: "InvalidBody", Message: "body must be bounded valid JSON with declared fields", Field: "body"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return &serving.Error{Code: "InvalidBody", Message: "one JSON object is required", Field: "body"}
	}
	return nil
}
func precondition(r *http.Request) (string, error) {
	version := strings.TrimSpace(r.Header.Get("If-Match"))
	if strings.HasPrefix(version, "\"") && strings.HasSuffix(version, "\"") && len(version) > 1 {
		version = version[1 : len(version)-1]
	}
	if version == "" || version == "*" || strings.ContainsAny(version, ", \t\r\n") {
		return "", &serving.Error{Code: "ResourceVersionRequired", Message: "If-Match must contain the queried resourceVersion", Field: "If-Match"}
	}
	return version, nil
}

func (h *Handler) management(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	// Once a validated mutation starts, a lost caller does not cancel its saved
	// management operation. Kubernetes calls still have the configured bound.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.config.RequestTimeout)
	defer cancel()
	id := r.PathValue("id")
	var value any
	var err error
	status := http.StatusOK
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v1/workloads"):
		switch r.Method {
		case http.MethodGet:
			if id == "" {
				value, err = h.manager.ListWorkloads(ctx)
			} else {
				value, err = h.manager.GetWorkload(ctx, id)
			}
		case http.MethodPost:
			var body struct {
				Name string           `json:"name"`
				Spec api.WorkloadSpec `json:"spec"`
			}
			err = h.decode(w, r, &body)
			if err == nil {
				value, err = h.manager.CreateWorkload(ctx, body.Name, body.Spec)
				status = http.StatusCreated
			}
		case http.MethodPut:
			var body struct {
				Spec api.WorkloadSpec `json:"spec"`
			}
			var version string
			var accepted bool
			version, err = precondition(r)
			if err == nil {
				err = h.decode(w, r, &body)
			}
			if err == nil {
				value, accepted, err = h.manager.UpdateWorkload(ctx, id, version, body.Spec)
				if accepted {
					status = http.StatusAccepted
				}
			}
		case http.MethodDelete:
			var version string
			version, err = precondition(r)
			if err == nil {
				value, err = h.manager.DeleteWorkload(ctx, id, version)
				status = http.StatusAccepted
			}
		}
	case strings.HasPrefix(r.URL.Path, "/api/v1/models"):
		switch r.Method {
		case http.MethodGet:
			if id == "" {
				value, err = h.manager.ListModels(ctx)
			} else {
				value, err = h.manager.GetModel(ctx, id)
			}
		case http.MethodPost:
			var body struct {
				Name string            `json:"name"`
				Spec api.ModelSpecSpec `json:"spec"`
			}
			err = h.decode(w, r, &body)
			if err == nil {
				value, err = h.manager.CreateModel(ctx, body.Name, body.Spec)
				status = http.StatusCreated
			}
		case http.MethodPut:
			var body struct {
				Spec api.ModelSpecSpec `json:"spec"`
			}
			var version string
			var accepted bool
			version, err = precondition(r)
			if err == nil {
				err = h.decode(w, r, &body)
			}
			if err == nil {
				value, accepted, err = h.manager.UpdateModel(ctx, id, version, body.Spec)
				if accepted {
					status = http.StatusAccepted
				}
			}
		case http.MethodDelete:
			var version string
			version, err = precondition(r)
			if err == nil {
				err = h.manager.DeleteModel(ctx, id, version)
				status = http.StatusNoContent
			}
		}
	case strings.HasPrefix(r.URL.Path, "/api/v1/pools"):
		value, err = h.poolView(id)
	}
	if err != nil {
		sendError(w, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	sendJSON(w, status, value)
}

func (h *Handler) poolView(id string) (any, error) {
	pools := h.runtime.Pools()
	if id == "" {
		return struct {
			Items  []serving.PoolState         `json:"items"`
			Issues []servingkube.ObserverIssue `json:"issues"`
		}{pools, h.observer.Issues()}, nil
	}
	for _, pool := range pools {
		if pool.ID == id {
			return pool, nil
		}
	}
	return nil, &serving.Error{Code: "PoolNotFound", Message: "no current pool has this identifier"}
}

func (h *Handler) infer(w http.ResponseWriter, r *http.Request) {
	received := time.Now()
	receipt, receiveErr := h.runtime.Receive(r.PathValue("id"), received)
	if receipt.ID != "" {
		w.Header().Set("X-Chill-Request-ID", receipt.ID)
	}
	controller := http.NewResponseController(w)
	finish := func(outcome, code string) {
		if err := h.runtime.Response(receipt, outcome, code); err != nil {
			ctrl.Log.Error(err, "persist inference response outcome", "requestID", receipt.ID)
		}
	}
	completeError := func(err error, outcome string) {
		if r.Context().Err() != nil {
			err = contextRequestError(r.Context().Err())
			outcome = contextOutcome(r.Context().Err())
		}
		if errors.Is(r.Context().Err(), context.Canceled) {
			finish(outcome, errorCode(err))
			return
		}
		if writeErr := writeError(w, err); writeErr != nil {
			finish("delivery_failed", "ResponseDeliveryFailed")
			return
		}
		if flushErr := controller.Flush(); flushErr != nil {
			finish("delivery_failed", "ResponseDeliveryFailed")
			return
		}
		finish(outcome, errorCode(err))
	}
	timeout := h.config.RequestTimeout
	if requested := r.Header.Get("X-Chill-Request-Timeout"); requested != "" {
		duration, err := time.ParseDuration(requested)
		if err != nil || duration <= 0 || duration > timeout {
			completeError(&serving.Error{Code: "InvalidRequestTimeout", Message: "explicit timeout must be positive and at most the configured request lifetime", Field: "X-Chill-Request-Timeout"}, "rejected")
			return
		}
		timeout = duration
	}
	deadline := received.Add(timeout)
	receipt.Deadline = deadline
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	r = r.WithContext(ctx)
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		completeError(err, "error")
		return
	}
	if err := h.authorize(r); err != nil {
		completeError(err, "rejected")
		return
	}
	if receipt.UID != "" {
		w.Header().Set("X-Chill-Workload-UID", receipt.UID)
	}
	if receiveErr != nil {
		completeError(receiveErr, "rejected")
		return
	}
	var input serving.InferenceInput
	if err := h.decode(w, r, &input); err != nil {
		completeError(err, "rejected")
		return
	}
	if ctx.Err() != nil {
		completeError(contextRequestError(ctx.Err()), contextOutcome(ctx.Err()))
		return
	}
	call, err := h.runtime.Accept(receipt, &input)
	if err != nil {
		completeError(err, "rejected")
		return
	}
	result, err := call.Wait(ctx)
	if err != nil {
		outcome := "error"
		switch errorCode(err) {
		case requestTimeoutCode, "SLODeadlineDrop":
			outcome = "timeout"
		case "RequestCanceled":
			outcome = "canceled"
		}
		completeError(err, outcome)
		return
	}
	if ctx.Err() != nil {
		completeError(contextRequestError(ctx.Err()), contextOutcome(ctx.Err()))
		return
	}
	w.Header().Set("Content-Type", result.ContentType)
	status := result.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, writeErr := w.Write(result.Body)
	if writeErr == nil {
		writeErr = controller.Flush()
	}
	if writeErr != nil || r.Context().Err() != nil {
		finish("delivery_failed", "ResponseDeliveryFailed")
		return
	}
	finish("completed", "")
}
func contextOutcome(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "canceled"
}
func contextRequestError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &serving.Error{Code: requestTimeoutCode, Message: "explicit request lifetime reached"}
	}
	return &serving.Error{Code: "RequestCanceled", Message: "caller canceled the request"}
}

func (h *Handler) observations(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	query := r.URL.Query()
	var from, to time.Time
	var err error
	if text := query.Get("from"); text != "" {
		from, err = time.Parse(time.RFC3339Nano, text)
	}
	if err == nil {
		if text := query.Get("to"); text != "" {
			to, err = time.Parse(time.RFC3339Nano, text)
		}
	}
	limit := h.config.MaxQueryEvents
	if err == nil {
		if text := query.Get("limit"); text != "" {
			limit, err = strconv.Atoi(text)
			if limit <= 0 || limit > h.config.MaxQueryEvents {
				err = errors.New("query limit exceeds configured bound")
			}
		}
	}
	if err != nil {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "valid RFC3339 interval and bounded positive event limit are required"})
		return
	}
	result, err := h.journal.Query(r.PathValue("uid"), from, to, query["definition"], query["outcome"], limit, query.Get("cursor"))
	if err != nil {
		var domain *serving.Error
		if !errors.As(err, &domain) {
			err = &serving.Error{Code: "ObservationUnavailable", Message: "observation journal could not be read"}
		}
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, result)
}

func errorCode(err error) string {
	var domain *serving.Error
	if errors.As(err, &domain) {
		return domain.Code
	}
	switch {
	case errors.Is(err, servingkube.ErrUnauthenticated):
		return "Unauthenticated"
	case errors.Is(err, servingkube.ErrForbidden):
		return "Forbidden"
	case errors.Is(err, servingkube.ErrAuthorizationUnavailable):
		return "AuthorizationUnavailable"
	case apierrors.IsNotFound(err):
		return "NotFound"
	case apierrors.IsAlreadyExists(err):
		return "AlreadyExists"
	case apierrors.IsConflict(err):
		return "ResourceVersionConflict"
	}
	return "ServiceUnavailable"
}
func writeError(w http.ResponseWriter, err error) error {
	code := errorCode(err)
	status := http.StatusServiceUnavailable
	switch code {
	case "Unauthenticated":
		status = http.StatusUnauthorized
	case "Forbidden":
		status = http.StatusForbidden
	case "NotFound", "WorkloadNotFound", "PoolNotFound":
		status = http.StatusNotFound
	case "InvalidBody", "InvalidInput", "InvalidWorkload", "InvalidModel", "InvalidModelReference", "InvalidQuery", "InvalidRequestTimeout", "ResourceVersionRequired":
		status = http.StatusBadRequest
	case "AlreadyExists", "ModelReferenced", "ModelDeleting", "ResourceVersionConflict", "OperationConflict", "RegistrationConflict", "ModelIdentityConflict", "DefinitionChanged", "WorkloadChanged":
		status = http.StatusConflict
	case "QueueFull":
		status = http.StatusTooManyRequests
	case requestTimeoutCode:
		status = http.StatusGatewayTimeout
	case "RequestCanceled", "SLODeadlineDrop":
		status = http.StatusRequestTimeout
	case "BackendError", "BackendUnavailable", "BackendResponseInvalid", "BackendResponseTooLarge", "BackendContextMismatch":
		status = http.StatusBadGateway
	}
	result := serving.Error{Code: code, Message: "service could not complete this request"}
	var domain *serving.Error
	if errors.As(err, &domain) {
		result = *domain
	} else if status == http.StatusUnauthorized || status == http.StatusForbidden {
		result.Message = http.StatusText(status)
	} else if status == http.StatusNotFound || status == http.StatusConflict {
		result.Message = err.Error()
	}
	return writeJSON(w, status, result)
}
func writeJSON(w http.ResponseWriter, status int, value any) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(value)
}

func sendJSON(w http.ResponseWriter, status int, value any) {
	if err := writeJSON(w, status, value); err != nil {
		ctrl.Log.Error(err, "deliver HTTP response")
	}
}

func sendError(w http.ResponseWriter, cause error) {
	if err := writeError(w, cause); err != nil {
		ctrl.Log.Error(err, "deliver HTTP error response")
	}
}
