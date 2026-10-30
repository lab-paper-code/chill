package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/serving"
)

const rawObservationsPath = "/api/v1/observations/raw"

func (h *Handler) rawObservations(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil { sendError(w, err); return }
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	invalid := func() { sendError(w, &serving.Error{Code: "InvalidQuery", Message: "a raw hour query, hour/kind path, or definitions path without extra parameters is required"}) }
	if r.URL.RawPath != "" { invalid(); return }
	if r.URL.Path == rawObservationsPath {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 1 || len(query["hour"]) != 1 { invalid(); return }
		index, err := h.observationLog.RawHour(r.Context(), query.Get("hour"))
		if err != nil { rawObservationError(w, err); return }
		sendJSON(w, http.StatusOK, index)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery { invalid(); return }
	var raw serving.RawObservation
	var err error
	path := strings.TrimPrefix(r.URL.Path, rawObservationsPath+"/")
	if path == "definitions" {
		raw, err = h.observationLog.OpenRawDefinitions(r.Context())
	} else {
		parts := strings.Split(path, "/")
		if len(parts) != 2 { invalid(); return }
		raw, err = h.observationLog.OpenRaw(r.Context(), parts[0], parts[1])
	}
	if err != nil { rawObservationError(w, err); return }
	defer raw.Reader.Close()
	if err := r.Context().Err(); err != nil { sendError(w, contextRequestError(err)); return }
	w.Header().Set("Content-Type", raw.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(raw.Bytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.CopyN(w, raw.Reader, raw.Bytes); err != nil {
		// Do not append JSON to a partial JSONL/gzip response. Abort the HTTP
		// stream even if a buffering writer has not flushed the first bytes.
		panic(http.ErrAbortHandler)
	}
	if err := raw.Reader.Close(); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func rawObservationError(w http.ResponseWriter, err error) {
	var domain *serving.Error
	if errors.As(err, &domain) && domain.Code == "ObservationUnavailable" {
		sendJSON(w, http.StatusNotFound, domain)
		return
	}
	sendError(w, err)
}
