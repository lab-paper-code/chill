package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
			sendJSON(w, http.StatusOK, h.config.Scheduler.Settings())
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
	if h.config.RefreshScheduler == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler configuration refresh is not enabled"})
		return
	}
	principal := servingkube.PrincipalFromContext(r.Context())
	if err := h.authorizer.AuthorizeConfigMapUpdate(r.Context(), principal,
		h.config.SchedulerNamespace, h.config.SchedulerConfigMap); err != nil {
		sendError(w, err)
		return
	}
	var input serving.SchedulerSettingsInput
	if err := h.decode(w, r, &input); err != nil {
		sendError(w, err)
		return
	}
	settings, err := input.Settings()
	if err != nil {
		sendError(w, &serving.Error{Code: "InvalidBody", Message: err.Error(), Field: "settings"})
		return
	}
	current := map[string]bool{}
	for _, d := range h.runtime.SchedulerInputs(time.Now).Workloads {
		current[d.UID] = true
	}
	// The stored list names only current Workloads.
	settings.AutonomousWorkloads = slices.DeleteFunc(slices.Clone(settings.AutonomousWorkloads), func(uid string) bool { return !current[uid] })
	h.config.SchedulerLock.Lock()
	defer h.config.SchedulerLock.Unlock()
	username, uid := principal.Actor()
	now := time.Now().UTC()
	change := serving.SchedulerSettingsChange{
		ActorUsername: username, ActorUID: uid, Before: h.config.Scheduler.Settings(), After: settings,
		ValidatedAt: now, Affected: []serving.NodeReference{},
	}
	for _, node := range h.config.Scheduler.State().Snapshot.Nodes {
		change.Affected = append(change.Affected, node.Node)
	}
	// Persist intent before touching either ConfigMap or running policy.
	if err := h.observationLog.SchedulerRecord(serving.SchedulerObservation{At: now, Settings: &change}); err != nil {
		sendError(w, err)
		return
	}
	err = persistSchedulerSettings(r.Context(), h.authorizer.Client, h.config.SchedulerNamespace,
		h.config.SchedulerConfigMap, h.config.SchedulerConfigKey, settings)
	if err == nil {
		// Only the scheduler service's full refresh can accept a configuration. Applying
		// settings alone could enable a catalog whose selection was removed by
		// a previously persisted change that has not refreshed successfully.
		err = h.config.RefreshScheduler(r.Context())
	}
	if err != nil {
		change.Error = err.Error()
	} else {
		applied := time.Now().UTC()
		change.AppliedAt = &applied
	}
	recordErr := h.observationLog.SchedulerRecord(serving.SchedulerObservation{At: time.Now().UTC(), Settings: &change})
	if err = errors.Join(err, recordErr); err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, change)
}

func persistSchedulerSettings(
	ctx context.Context, kube kubernetes.Interface, namespace, name, key string, settings serving.SchedulerSettings,
) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	cm, err := kube.CoreV1().ConfigMaps(namespace).Get(ctx, name, meta.GetOptions{})
	if err != nil {
		return err
	}
	var config map[string]json.RawMessage
	if err = json.Unmarshal([]byte(cm.Data[key]), &config); err != nil {
		return err
	}
	if config == nil {
		return errors.New("scheduler configuration must be an object")
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	config["settings"] = raw
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	cm.Data[key] = string(encoded)
	_, err = kube.CoreV1().ConfigMaps(namespace).Update(ctx, cm, meta.UpdateOptions{})
	return err
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
	window := h.config.ObservationWindow
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
		result, err := h.observationLog.LoadWindow(from, to)
		if err != nil {
			sendError(w, err)
			return
		}
		result.Arrivals = nil
		sendJSON(w, http.StatusOK, result)
		return
	}
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
	records, nextOffset, err := h.observationLog.SchedulerObservationPage(from, to, limit, offset)
	if err != nil {
		sendError(w, err)
		return
	}
	next := ""
	if nextOffset >= 0 {
		next = strconv.Itoa(nextOffset)
	}
	if compact {
		evidence := newSchedulerReadEvidence()
		events := evidence.observations(records)
		sendJSON(w, http.StatusOK, struct {
			From       time.Time                  `json:"from"`
			To         time.Time                  `json:"to"`
			Events     []schedulerReadObservation `json:"events"`
			NextCursor string                     `json:"nextCursor,omitempty"`
			schedulerReadEvidence
		}{from, to, events, next, evidence})
		return
	}
	sendJSON(w, http.StatusOK, struct {
		From       time.Time                      `json:"from"`
		To         time.Time                      `json:"to"`
		Events     []serving.SchedulerObservation `json:"events"`
		NextCursor string                         `json:"nextCursor,omitempty"`
	}{from, to, records, next})
}
