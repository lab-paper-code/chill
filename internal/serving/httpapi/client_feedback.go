package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

const ClientFeedbackPath = "/api/v1/workloads/{id}/feedback"

// ClientObservationReport contains only client-owned measurements and identities.
type ClientObservationReport struct {
	ObservationID string `json:"observationID"`
	RequestID     string `json:"requestID,omitempty"`
	WorkloadUID   string `json:"workloadUID,omitempty"`
	ElapsedNanos  *int64 `json:"elapsedNanos"`
	Outcome       string `json:"outcome"`
	StatusCode    *int   `json:"statusCode,omitempty"`
}

func (h *Handler) clientFeedback(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	var report ClientObservationReport
	if err := h.decode(w, r, &report); err != nil {
		sendError(w, err)
		return
	}
	if err := report.validate(); err != nil {
		sendError(w, err)
		return
	}
	actor, uid := servingkube.PrincipalFromContext(r.Context()).Actor()
	err := h.observationLog.ClientObservation(r.Context(), serving.ClientObservation{
		ObservationID: report.ObservationID, RequestID: report.RequestID,
		WorkloadName: r.PathValue("id"), WorkloadUID: report.WorkloadUID,
		ElapsedNanos: *report.ElapsedNanos, Outcome: report.Outcome, StatusCode: report.StatusCode,
		ActorUsername: actor, ActorUID: uid,
	})
	if err != nil {
		var domain *serving.Error
		if !errors.As(err, &domain) {
			err = &serving.Error{Code: "ObservationUnavailable", Message: "client observation could not be admitted"}
		}
		sendError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (report ClientObservationReport) validate() error {
	invalid := &serving.Error{Code: "InvalidClientObservation", Message: "nonempty observationID, nonnegative elapsedNanos and consistent outcome/statusCode are required"}
	if report.ObservationID == "" || report.ElapsedNanos == nil || *report.ElapsedNanos < 0 {
		return invalid
	}
	if report.StatusCode != nil && (*report.StatusCode < 100 || *report.StatusCode > 599) {
		return invalid
	}
	switch report.Outcome {
	case timingOutcomeCompleted:
		if report.StatusCode == nil || *report.StatusCode < 200 || *report.StatusCode >= 300 {
			return invalid
		}
	case "http_error":
		if report.StatusCode == nil || *report.StatusCode >= 200 && *report.StatusCode < 300 {
			return invalid
		}
	case timingOutcomeTimeout, "transport_error", timingOutcomeCanceled:
	default:
		return invalid
	}
	return nil
}

func (h *Handler) clientObservations(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "invalid client observation query"})
		return
	}
	from, to, limit, err := eventRangeQuery(query, h.config.MaxQueryEvents)
	offset := 0
	if err == nil && query.Get("cursor") != "" {
		offset, err = strconv.Atoi(query.Get("cursor"))
		if err != nil || offset < 0 {
			err = &serving.Error{Code: "InvalidQuery", Message: "nonnegative client observation cursor is required"}
		}
	}
	if err != nil {
		sendError(w, err)
		return
	}
	result, err := h.observationLog.ClientObservationPage(r.Context(), from, to, limit, offset)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, result)
}
