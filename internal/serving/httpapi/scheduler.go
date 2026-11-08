package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"

	"github.com/lab-paper-code/chill/internal/serving/application"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func (h *Handler) scheduler(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.Scheduler == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler configuration is not enabled"})
		return
	}
	if r.Method == http.MethodGet {
		compact, err := schedulerCompactQuery(r)
		if err != nil {
			sendError(w, err)
			return
		}
		if r.URL.Query().Get("view") == schedulerSummaryView {
			state := h.config.Scheduler.State()
			sendJSON(w, http.StatusOK, summarizeScheduler(&state))
			return
		}
		if compact && !strings.HasSuffix(r.URL.Path, "/settings") {
			state := h.config.Scheduler.State()
			evidence := newSchedulerReadEvidence()
			projected := evidence.state(&state)
			if strings.HasSuffix(r.URL.Path, "/state") {
				sendJSON(w, http.StatusOK, struct {
					schedulerReadState
					schedulerReadEvidence
				}{projected, evidence})
			} else {
				sendJSON(w, http.StatusOK, struct {
					Settings serving.SchedulerSettings `json:"settings"`
					State    schedulerReadState        `json:"state"`
					schedulerReadEvidence
				}{h.config.Scheduler.Settings(), projected, evidence})
			}
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/settings"):
			h.schedulerSettingsRead(w, r)
		case strings.HasSuffix(r.URL.Path, "/state"):
			sendJSON(w, http.StatusOK, h.config.Scheduler.State())
		default:
			sendJSON(w, http.StatusOK, struct {
				Settings serving.SchedulerSettings            `json:"settings"`
				State    servingkube.SchedulerControllerState `json:"state"`
			}{h.config.Scheduler.Settings(), h.config.Scheduler.State()})
		}
		return
	}
	if h.config.ChangeSchedulerSettings == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler configuration refresh is not enabled"})
		return
	}
	principal := servingkube.PrincipalFromContext(r.Context())
	if err := h.authorizer.AuthorizeConfigMapUpdate(r.Context(), principal,
		h.config.SchedulerNamespace, h.config.SchedulerConfigMap); err != nil {
		sendError(w, err)
		return
	}
	if r.Header.Get("If-Match") == "" {
		sendError(w, &serving.Error{Code: "SettingsPreconditionRequired", Message: "GET settings and supply its ETag in If-Match"})
		return
	}
	var input application.SettingsInput
	if err := h.decode(w, r, &input); err != nil {
		sendError(w, err)
		return
	}
	versionMatch, parseErr := strconv.Unquote(r.Header.Get("If-Match"))
	validMatch := len(r.Header.Values("If-Match")) == 1 && parseErr == nil && r.Header.Get("If-Match") == strconv.Quote(versionMatch)
	username, uid := principal.Actor()
	change, version, err := h.config.ChangeSchedulerSettings(r.Context(), input, versionMatch, validMatch, username, uid)
	if err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Quote(version))
	sendJSON(w, http.StatusOK, change)
}

func schedulerObservationSelectionQuery(query url.Values) (observation.SchedulerObservationSelection, error) {
	selection := observation.SchedulerObservationSelection{}
	for _, key := range []string{"order", "filter", "uid"} {
		values, present := query[key]
		if !present {
			continue
		}
		valid := len(values) == 1 && values[0] != ""
		if valid {
			switch key {
			case "order":
				selection.Order = values[0]
				valid = selection.Order == "asc" || selection.Order == "desc"
			case "filter":
				selection.Filter = values[0]
				switch selection.Filter {
				case "all", "decision-action", "decision", "action", "snapshot", "settings", "power", "other":
				default:
					valid = false
				}
			case "uid":
				selection.UID = values[0]
				valid = len(selection.UID) <= 256 && !strings.ContainsAny(selection.UID, " /\\\t\r\n")
				for _, char := range selection.UID {
					if char < 32 || char == 127 {
						valid = false
					}
				}
			}
		}
		if !valid {
			return selection, &serving.Error{Code: "InvalidQuery", Message: "invalid scheduler observation " + key, Field: key}
		}
	}
	return selection, nil
}

func (h *Handler) schedulerObservations(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	compact, err := schedulerCompactQuery(r)
	if err != nil {
		sendError(w, err)
		return
	}
	query := r.URL.Query()
	to := time.Now().UTC()
	window := time.Minute
	if h.config.Scheduler != nil {
		window = h.config.Scheduler.Settings().ObservationWindow
	}
	if text := query.Get("to"); text != "" {
		to, err = time.Parse(time.RFC3339Nano, text)
	}
	from := to.Add(-window)
	if err == nil {
		if text := query.Get("from"); text != "" {
			from, err = time.Parse(time.RFC3339Nano, text)
		}
	}
	if err != nil || !from.Before(to) {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "positive RFC3339 observation interval is required"})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/load") {
		result, err := h.observationLog.LoadWindow(r.Context(), from, to)
		if err != nil {
			sendError(w, err)
			return
		}
		result.Arrivals = nil
		sendJSON(w, http.StatusOK, result)
		return
	}
	selection, err := schedulerObservationSelectionQuery(query)
	if err != nil {
		sendError(w, err)
		return
	}
	selection.Summary = query.Get("view") == schedulerSummaryView
	limit := h.config.MaxQueryEvents
	offset := 0
	if text := query.Get("limit"); text != "" {
		limit, err = strconv.Atoi(text)
	}
	if err == nil {
		if text := query.Get("cursor"); text != "" {
			offset, err = strconv.Atoi(text)
		}
	}
	if err != nil || limit <= 0 || limit > h.config.MaxQueryEvents || offset < 0 {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "bounded limit and nonnegative cursor are required"})
		return
	}
	records, nextOffset, gapUntil, coverage, err := h.observationLog.SchedulerObservationPage(r.Context(), from, to, limit, offset, selection)
	if err != nil {
		sendError(w, err)
		return
	}
	next := ""
	if nextOffset >= 0 {
		next = strconv.Itoa(nextOffset)
	}
	if query.Get("view") == schedulerSummaryView {
		sendJSON(w, http.StatusOK, struct {
			From       time.Time                     `json:"from"`
			To         time.Time                     `json:"to"`
			Events     []schedulerHistoryObservation `json:"events"`
			NextCursor string                        `json:"nextCursor,omitempty"`
			GapUntil   *time.Time                    `json:"gapUntil,omitempty"`
			Coverage   serving.ObservationCoverage   `json:"coverage"`
		}{from, to, schedulerHistoryObservations(records), next, gapUntil, coverage})
		return
	}
	if compact {
		evidence := newSchedulerReadEvidence()
		events := evidence.observations(records)
		sendJSON(w, http.StatusOK, struct {
			From       time.Time                   `json:"from"`
			To         time.Time                   `json:"to"`
			Events     []schedulerReadObservation  `json:"events"`
			NextCursor string                      `json:"nextCursor,omitempty"`
			GapUntil   *time.Time                  `json:"gapUntil,omitempty"`
			Coverage   serving.ObservationCoverage `json:"coverage"`
			schedulerReadEvidence
		}{from, to, events, next, gapUntil, coverage, evidence})
		return
	}
	sendJSON(w, http.StatusOK, struct {
		From       time.Time                          `json:"from"`
		To         time.Time                          `json:"to"`
		Events     []observation.SchedulerObservation `json:"events"`
		NextCursor string                             `json:"nextCursor,omitempty"`
		GapUntil   *time.Time                         `json:"gapUntil,omitempty"`
		Coverage   serving.ObservationCoverage        `json:"coverage"`
	}{from, to, records, next, gapUntil, coverage})
}

func (h *Handler) schedulerSettingsRead(w http.ResponseWriter, r *http.Request) {
	// One ConfigMap response supplies the persisted body and ETag without the
	// service control lock. Meter declarations remain separately accepted state.
	cm, config, err := application.SchedulerConfigMap(r.Context(), h.authorizer.Client, h.config.SchedulerNamespace,
		h.config.SchedulerConfigMap, h.config.SchedulerConfigKey)
	if err != nil {
		sendError(w, err)
		return
	}
	var input serving.SchedulerSettingsInput
	decoder := json.NewDecoder(bytes.NewReader(config["settings"]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		sendError(w, err)
		return
	}
	settings, err := input.Settings()
	if err != nil {
		sendError(w, err)
		return
	}
	meters := []servingops.MeterDeclaration{}
	if h.config.SchedulerMeters != nil {
		meters = append(meters, h.config.SchedulerMeters()...)
	}
	w.Header().Set("ETag", strconv.Quote(cm.ResourceVersion))
	sendJSON(w, http.StatusOK, struct {
		serving.SchedulerSettings
		StaticRouting    json.RawMessage               `json:"staticRouting,omitempty"`
		PowerObservation json.RawMessage               `json:"powerObservation,omitempty"` // Stored operating-power policy; read-only.
		Meters           []servingops.MeterDeclaration `json:"meters"`
		RequestTimeout   time.Duration                 `json:"requestTimeout"` // Runtime --request-timeout, in nanoseconds; read-only.
	}{settings, config["staticRouting"], config["powerObservation"], meters, h.config.RequestTimeout})
}
