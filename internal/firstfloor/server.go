package firstfloor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
	"unicode"
	"unicode/utf8"
)

type handler struct {
	scheduler *Scheduler
	admission chan struct{}
}

// httpTimingWriter owns handler-only boundaries. Scheduler progress receives
// immutable copies, so a canceled handler never races with active native work.
type httpTimingWriter struct {
	http.ResponseWriter
	entered time.Time
	timing  HTTPRequestTiming
}

func (w *httpTimingWriter) responseTiming(progress *HTTPRequestTiming) *HTTPRequestTiming {
	timing := w.timing
	if progress != nil {
		timing = *progress
	}
	start := time.Since(w.entered).Nanoseconds()
	timing.ResponseWriteStartNanos = &start
	return &timing
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
	if id != r.RequestID || !identity(id) || !identity(r.WorkloadUID) || !identity(r.Definition) || (r.AdmissionPolicy != AdmissionReject && r.AdmissionPolicy != AdmissionDelay) || r.ReceivedAt.IsZero() || !durationOK(r.SenderElapsedMS) || !durationOK(r.CompletionMarginMS) || !durationOK(r.LifetimeMS) || !durationOK(r.SLO.TargetMS) || r.SLO.TargetMS <= 0 || !finite(r.SLO.Percentile) || r.SLO.Percentile <= 0 || r.SLO.Percentile > 100 {
		return errors.New("complete bounded immutable request identity, admission policy, receipt, elapsed, SLO and lifetime required")
	}
	return nil
}

// prepareInputs keeps each configured field's elements framed as uint32
// little-endian length plus bytes. Content checks belong to the gateway.
func prepareInputs(names []string, inputs []Field, size int) ([][]byte, int, error) {
	if len(names) != len(inputs) {
		return nil, 0, errors.New("every configured raw input field required exactly once")
	}
	data := make([][]byte, len(names))
	total := 0
	for i, name := range names {
		var f *Field
		for j := range inputs {
			if inputs[j].Name == name {
				if f != nil {
					return nil, 0, errors.New("duplicate input field name")
				}
				f = &inputs[j]
			}
		}
		if f == nil || len(f.Data) != size {
			return nil, 0, errors.New("input field name or element count differs from configuration")
		}
		n := 0
		for _, element := range f.Data {
			if len(element) > MaxFrameBytes-4-total-n {
				return nil, 0, errors.New("combined inputs exceed IPC frame cap")
			}
			n += 4 + len(element)
		}
		framed := make([]byte, 0, n)
		for _, element := range f.Data {
			framed = binary.LittleEndian.AppendUint32(framed, uint32(len(element)))
			framed = append(framed, element...)
		}
		data[i] = framed
		total += n
	}
	return data, total, nil
}
func (h *handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	entered := time.Now()
	cfg := h.scheduler.config.Load()
	base := "/v2/models/" + cfg.Model
	if req.Method == http.MethodGet && h.serveStatus(w, req, base) {
		return
	}
	timed := &httpTimingWriter{ResponseWriter: w, entered: entered, timing: HTTPRequestTiming{HandlerEnteredAt: entered.UTC()}}
	w = timed
	if req.URL.Path != base+"/infer" {
		writeError(w, http.StatusNotFound, errors.New("unknown path or model"))
		return
	}
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	if !h.scheduler.Ready() {
		writeError(w, http.StatusServiceUnavailable, ErrUnavailable)
		return
	}
	received := time.Now()
	receivedNanos := received.Sub(entered).Nanoseconds()
	timed.timing.ReceivedNanos = &receivedNanos
	select {
	case h.admission <- struct{}{}:
		defer func() { <-h.admission }()
	default:
		writeNativeFailure(w, http.StatusTooManyRequests, ErrorResponse{Error: ErrQueueFull.Error(), Code: CodeQueueFull, Stage: AdmissionStageExecutor, NotAdmitted: true, ExecutionComplete: true})
		return
	}
	// Preserve net/http's original writer hooks for oversized request bodies.
	req.Body = http.MaxBytesReader(timed.ResponseWriter, req.Body, int64(cfg.MaxRequestBytes))
	defer func() {
		if err := req.Body.Close(); err != nil {
			slog.Warn("close request body", "error", err)
		}
	}()
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	r := &request{received: received, handlerEntered: entered, httpTiming: &timed.timing}
	if !prepareInference(w, req.Context(), decoder, cfg, r) {
		return
	}
	if r.bytes > cfg.MaxQueueBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("input exceeds queue byte cap"))
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
			writeError(w, http.StatusServiceUnavailable, ErrUnavailable)
		}
	case base + "/metadata", base + "/state":
		state, e := h.scheduler.State(req.Context())
		if e != nil {
			writeError(w, http.StatusServiceUnavailable, e)
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
func decodeRequest(w http.ResponseWriter, decoder *json.Decoder, v any) error {
	if timed, ok := w.(*httpTimingWriter); ok {
		start := time.Since(timed.entered).Nanoseconds()
		timed.timing.DecodeStartNanos = &start
		defer func() {
			done := time.Since(timed.entered).Nanoseconds()
			timed.timing.DecodeDoneNanos = &done
		}()
	}
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
func prepareInference(w http.ResponseWriter, ctx context.Context, decoder *json.Decoder, cfg *Config, r *request) bool {
	var inference Request
	if e := decodeRequest(w, decoder, &inference); e != nil {
		bodyError(w, ctx, e)
		return false
	}
	if e := validateContext(inference.Context, inference.ID); e != nil {
		writeError(w, http.StatusBadRequest, e)
		return false
	}
	d, n, e := prepareInputs(cfg.Inputs, inference.Inputs, 1)
	if e != nil {
		writeError(w, http.StatusBadRequest, e)
		return false
	}
	r.data = d
	r.bytes = n
	r.context = inference.Context
	if r.context.LifetimeMS > 0 {
		remaining := r.context.LifetimeMS - r.context.SenderElapsedMS - r.context.CompletionMarginMS
		if remaining <= 0 {
			writeFailure(w, http.StatusRequestTimeout, context.DeadlineExceeded, true, nil)
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
		var failedItem *preprocessError
		var failedBatch *batchError
		var progress *Progress
		switch {
		case errors.As(v.err, &failedItem):
			status = http.StatusUnprocessableEntity
		case errors.As(v.err, &failedBatch):
			status = http.StatusInternalServerError
		case errors.Is(v.err, ErrQueueFull):
			status = http.StatusTooManyRequests
		case errors.Is(v.err, ErrDeadlineInfeasible):
			status = http.StatusServiceUnavailable
		case errors.Is(v.err, context.DeadlineExceeded) || errors.Is(v.err, context.Canceled):
			status = http.StatusRequestTimeout
		}
		if failedItem != nil || failedBatch != nil || errors.Is(v.err, ErrDeadlineInfeasible) || errors.Is(v.err, ErrQueueFull) {
			progress = &v.response.Progress
		}
		if errors.Is(v.err, ErrDeadlineInfeasible) || errors.Is(v.err, ErrQueueFull) {
			code := CodeDeadlineInfeasible
			if errors.Is(v.err, ErrQueueFull) {
				code = CodeQueueFull
			}
			remaining := int64(RemainingSLOMS(RequestState{Context: r.context, ElapsedMS: progress.NativeElapsedMS}) * float64(time.Millisecond))
			writeNativeFailure(w, status, ErrorResponse{Error: v.err.Error(), Code: code, Stage: AdmissionStageExecutor, Policy: r.context.AdmissionPolicy, RemainingSLONanos: &remaining, ExecutorWaitNanos: progress.ExecutorWaitNanos, ExecutionComplete: v.complete, Progress: progress})
		} else {
			writeFailure(w, status, v.err, v.complete, progress)
		}
		return
	}
	writeJSON(w, v.response)
}
func bodyError(w http.ResponseWriter, ctx context.Context, e error) {
	status := http.StatusBadRequest
	var big *http.MaxBytesError
	if errors.As(e, &big) {
		status = http.StatusRequestEntityTooLarge
	} else if ctx.Err() != nil {
		status = http.StatusRequestTimeout
	}
	writeError(w, status, e)
}
func writeJSON(w http.ResponseWriter, v any) {
	if timed, ok := w.(*httpTimingWriter); ok {
		if response, ok := v.(Response); ok {
			response.Progress.NativeHTTP = timed.responseTiming(response.Progress.NativeHTTP)
			v = response
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write JSON response", "error", err)
	}
}
func writeError(w http.ResponseWriter, status int, e error) {
	writeFailure(w, status, e, true, nil)
}
func writeFailure(w http.ResponseWriter, status int, e error, complete bool, p *Progress) {
	s := e.Error()
	if len(s) > MaxErrorBytes {
		s = "error exceeds detail bound"
	}
	var failedBatch *batchError
	var failureReason string
	if errors.As(e, &failedBatch) {
		failureReason = failedBatch.failureReason
	}
	failure := ErrorResponse{Error: s, FailureReason: failureReason, ExecutionComplete: complete, Progress: p}
	if errors.Is(e, context.DeadlineExceeded) {
		failure.Stage = AdmissionStageExecutor
		failure.Code = CodeRequestTimeout
	}
	writeNativeFailure(w, status, failure)
}
func writeNativeFailure(w http.ResponseWriter, status int, failure ErrorResponse) {
	if timed, ok := w.(*httpTimingWriter); ok {
		if failure.Progress != nil {
			p := *failure.Progress
			p.NativeHTTP = timed.responseTiming(p.NativeHTTP)
			failure.Progress = &p
		} else {
			failure.NativeHTTP = timed.responseTiming(nil)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(failure); err != nil {
		slog.Warn("write error response", "error", err)
	}
}
