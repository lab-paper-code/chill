package firstfloor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"time"
	"unicode"
	"unicode/utf8"
)

type handler struct {
	scheduler *Scheduler
	admission chan struct{}
}

func NewHandler(s *Scheduler) http.Handler {
	return &handler{s, make(chan struct{}, s.config.Load().MaxQueueRequests)}
}
func identity(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
func validateContext(r RequestContext, id string) error {
	if id != r.RequestID || !identity(id) || !identity(r.WorkloadUID) || !identity(r.Definition) || r.ReceivedAt.IsZero() || !durationOK(r.SenderElapsedMS) || !durationOK(r.LifetimeMS) || !durationOK(r.SLO.TargetMS) || r.SLO.TargetMS <= 0 || !finite(r.SLO.Percentile) || r.SLO.Percentile <= 0 || r.SLO.Percentile > 100 {
		return errors.New("complete bounded immutable request identity, receipt, elapsed, SLO and lifetime required")
	}
	return nil
}
func prepareInputs(specs []InputSpec, inputs []Tensor, size int) ([][]byte, int, error) {
	if len(specs) != len(inputs) {
		return nil, 0, errors.New("every named model input required")
	}
	data := make([][]byte, len(specs))
	total := 0
	for i, s := range specs {
		var t *Tensor
		for j := range inputs {
			if inputs[j].Name == s.Name {
				if t != nil {
					return nil, 0, errors.New("duplicate tensor name")
				}
				t = &inputs[j]
			}
		}
		if t == nil || t.Datatype != s.Datatype || !reflect.DeepEqual(t.Shape, append([]int{size}, s.Shape...)) {
			return nil, 0, errors.New("input name, datatype or single-sample shape differs from initialized model")
		}
		b, e := encodeTensor(*t)
		if e != nil {
			return nil, 0, e
		}
		if len(b) > MaxFrameBytes-total {
			return nil, 0, errors.New("combined inputs exceed IPC frame cap")
		}
		data[i] = b
		total += len(b)
	}
	return data, total, nil
}
func (h *handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	cfg := h.scheduler.config.Load()
	base := "/v2/models/" + cfg.Model
	if req.Method == http.MethodGet && h.serveStatus(w, req, base) {
		return
	}
	if req.URL.Path != base+"/infer" && req.URL.Path != base+"/profile" {
		writeError(w, http.StatusNotFound, errors.New("unknown path or model"), true)
		return
	}
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST required"), true)
		return
	}
	if !h.scheduler.Ready() {
		writeError(w, http.StatusServiceUnavailable, ErrUnavailable, true)
		return
	}
	received := time.Now()
	select {
	case h.admission <- struct{}{}:
		defer func() { <-h.admission }()
	default:
		writeError(w, http.StatusTooManyRequests, ErrQueueFull, true)
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, int64(cfg.MaxRequestBytes))
	defer func() {
		if err := req.Body.Close(); err != nil {
			slog.Warn("close request body", "error", err)
		}
	}()
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	r := &request{received: received}
	if req.URL.Path == base+"/profile" {
		if !prepareProfile(w, req.Context(), decoder, cfg, r) {
			return
		}
	} else if !prepareInference(w, req.Context(), decoder, cfg, r) {
		return
	}
	if r.bytes > cfg.MaxQueueBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("input exceeds queue byte cap"), true)
		return
	}
	h.submitRequest(w, req, r)
}
func (h *handler) serveStatus(w http.ResponseWriter, req *http.Request, base string) bool {
	switch req.URL.Path {
	case "/v2/health/live":
		w.WriteHeader(http.StatusOK)
	case "/v2/health/ready":
		if h.scheduler.Ready() {
			w.WriteHeader(http.StatusOK)
		} else {
			writeError(w, http.StatusServiceUnavailable, ErrUnavailable, true)
		}
	case base + "/metadata", base + "/state":
		state, e := h.scheduler.State(req.Context())
		if e != nil {
			writeError(w, http.StatusServiceUnavailable, e, true)
			return true
		}
		if req.URL.Path == base+"/metadata" {
			writeJSON(w, state.Metadata)
		} else {
			writeJSON(w, state)
		}
	default:
		return false
	}
	return true
}
func decodeRequest(decoder *json.Decoder, v any) error {
	if e := decoder.Decode(v); e != nil {
		return e
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		if e != nil {
			return e
		}
		return errors.New("exactly one request JSON object required")
	}
	return nil
}
func prepareProfile(w http.ResponseWriter, ctx context.Context, decoder *json.Decoder, cfg *Config, r *request) bool {
	if cfg.Mode != modeProfiling {
		writeError(w, http.StatusConflict, errors.New("fixed K endpoint requires profiling mode"), true)
		return false
	}
	var p ProfileRequest
	if e := decodeRequest(decoder, &p); e != nil {
		bodyError(w, ctx, e)
		return false
	}
	if !initialized(cfg.BatchSizes, p.BatchSize) || p.Executions < 1 || p.Executions > 10000 {
		writeError(w, http.StatusBadRequest, errors.New("initialized fixed batch and 1..10000 executions required"), true)
		return false
	}
	d, n, e := prepareInputs(cfg.Inputs, p.Inputs, p.BatchSize)
	if e != nil {
		writeError(w, http.StatusBadRequest, e, true)
		return false
	}
	p.Inputs = nil
	r.data = d
	r.bytes = n
	r.profile = &p
	r.context = RequestContext{RequestID: "profile-" + r.received.UTC().Format("150405.000000000"), WorkloadUID: modeProfiling, Definition: "fixed-k"}
	return true
}
func prepareInference(w http.ResponseWriter, ctx context.Context, decoder *json.Decoder, cfg *Config, r *request) bool {
	if cfg.Mode != modeServing {
		writeError(w, http.StatusConflict, errors.New("inference endpoint requires serving mode"), true)
		return false
	}
	var inference Request
	if e := decodeRequest(decoder, &inference); e != nil {
		bodyError(w, ctx, e)
		return false
	}
	if e := validateContext(inference.Context, inference.ID); e != nil {
		writeError(w, http.StatusBadRequest, e, true)
		return false
	}
	d, n, e := prepareInputs(cfg.Inputs, inference.Inputs, 1)
	if e != nil {
		writeError(w, http.StatusBadRequest, e, true)
		return false
	}
	r.data = d
	r.bytes = n
	r.context = inference.Context
	if r.context.LifetimeMS > 0 {
		remaining := r.context.LifetimeMS - r.context.SenderElapsedMS - remainingCompletionOverheadMS(cfg.Policy, r.context.SenderElapsedMS)
		if remaining <= 0 {
			writeError(w, http.StatusRequestTimeout, context.DeadlineExceeded, true)
			return false
		}
		r.lifetime = r.received.Add(time.Duration(remaining * float64(time.Millisecond)))
	}
	return true
}
func (h *handler) submitRequest(w http.ResponseWriter, req *http.Request, r *request) {
	// Lifetime cancels transport as well as queue waiting; an active physical batch
	// continues under the worker context and reports actual completion later.
	callerCtx := req.Context()
	if !r.lifetime.IsZero() {
		var cancel context.CancelFunc
		callerCtx, cancel = context.WithDeadline(callerCtx, r.lifetime)
		defer cancel()
	}
	v := h.scheduler.submit(callerCtx, r)
	if v.err != nil {
		if req.Context().Err() != nil {
			return
		}
		status := http.StatusServiceUnavailable
		if errors.Is(v.err, ErrQueueFull) {
			status = http.StatusTooManyRequests
		} else if errors.Is(v.err, context.DeadlineExceeded) || errors.Is(v.err, context.Canceled) {
			status = http.StatusRequestTimeout
		}
		if errors.Is(v.err, ErrDeadlineDrop) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			writeJSON(w, ErrorResponse{Error: v.err.Error(), ExecutionComplete: v.complete,
				Progress: &v.response.Progress})
		} else {
			writeError(w, status, v.err, v.complete)
		}
		return
	}
	if r.profile != nil {
		writeJSON(w, v.profile)
	} else {
		writeJSON(w, v.response)
	}
}
func bodyError(w http.ResponseWriter, ctx context.Context, e error) {
	status := http.StatusBadRequest
	var big *http.MaxBytesError
	if errors.As(e, &big) {
		status = http.StatusRequestEntityTooLarge
	} else if ctx.Err() != nil {
		status = http.StatusRequestTimeout
	}
	writeError(w, status, e, true)
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write JSON response", "error", err)
	}
}
func writeError(w http.ResponseWriter, status int, e error, complete bool) {
	s := e.Error()
	if len(s) > MaxErrorBytes {
		s = "error exceeds detail bound"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(ErrorResponse{Error: s, ExecutionComplete: complete}); err != nil {
		slog.Warn("write error response", "error", err)
	}
}
