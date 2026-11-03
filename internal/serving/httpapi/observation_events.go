package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

type observationEvent struct {
	Type string    `json:"type"`
	At   time.Time `json:"at"`
	live.Event
}

func (h *Handler) observationEvents(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "invalid event query"})
		return
	}
	var records []observation.RecordedEvent
	var next string
	var coverage *serving.ObservationCoverage
	var from, to time.Time
	_, rangedFrom := query["from"]
	_, rangedTo := query["to"]
	if rangedFrom || rangedTo {
		var limit int
		from, to, limit, err = eventRangeQuery(query, h.config.MaxQueryEvents)
		if err == nil {
			var available serving.ObservationCoverage
			records, next, available, err = h.observationLog.EventObservationPage(r.Context(), from, to, limit, query.Get("cursor"))
			coverage = &available
		}
	} else {
		at, parseErr := time.Parse(time.RFC3339Nano, query.Get("at"))
		if parseErr != nil || at.IsZero() || len(query["at"]) != 1 || len(query) > 2 || len(query) == 2 && (len(query["cursor"]) != 1 || query.Get("cursor") == "") {
			err = &serving.Error{Code: "InvalidQuery", Message: "one RFC3339Nano event timestamp is required"}
		} else {
			var available serving.ObservationCoverage
			records, next, available, err = h.observationLog.EventObservations(r.Context(), at, h.config.MaxQueryEvents, query.Get("cursor"))
			coverage = &available
		}
	}
	if err != nil {
		sendError(w, err)
		return
	}
	events := make([]observationEvent, 0, len(records))
	if records == nil {
		events = nil
	}
	for _, record := range records {
		event := observationEvent{Type: "event", At: record.At}
		if action := record.Action; action != nil {
			projected, err := ProjectAction(*action)
			if err != nil {
				sendError(w, err)
				return
			}
			event.Kind, event.Action = "action", projected
		} else if cluster := record.Cluster; cluster != nil {
			projected := &live.ClusterEvent{Kind: cluster.Kind, At: cluster.At.UTC().Format(time.RFC3339Nano), Event: cluster.Event, Source: cluster.Source, Reason: cluster.Reason, PreviousReason: cluster.PreviousReason}
			if pod := cluster.Pod; pod != nil {
				projected.Pod = &live.ClusterPod{Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID, Node: pod.Node, Phase: pod.Phase, Ready: pod.Ready}
			}
			event.Kind, event.Cluster = "cluster", projected
		}
		events = append(events, event)
	}
	response := struct {
		Events     []observationEvent           `json:"events"`
		From       *time.Time                   `json:"from,omitempty"`
		To         *time.Time                   `json:"to,omitempty"`
		NextCursor string                       `json:"nextCursor,omitempty"`
		Coverage   *serving.ObservationCoverage `json:"coverage,omitempty"`
	}{Events: events, NextCursor: next, Coverage: coverage}
	if rangedFrom || rangedTo {
		response.From, response.To = &from, &to
	}
	sendJSON(w, http.StatusOK, response)
}

func eventRangeQuery(query url.Values, maximum int) (time.Time, time.Time, int, error) {
	invalid := &serving.Error{Code: "InvalidQuery", Message: "from/to within 24 hours, bounded limit and event cursor are required"}
	for key, values := range query {
		if key != "from" && key != "to" && key != "limit" && key != "cursor" || len(values) != 1 || values[0] == "" {
			return time.Time{}, time.Time{}, 0, invalid
		}
	}
	from, fromErr := time.Parse(time.RFC3339Nano, query.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, query.Get("to"))
	maximum = min(maximum, 500)
	limit := min(100, maximum)
	var err error
	if text := query.Get("limit"); text != "" {
		limit, err = strconv.Atoi(text)
	}
	if fromErr != nil || toErr != nil || from.IsZero() || !from.Before(to) || to.Sub(from) > observation.EventObservationWindow || err != nil || limit <= 0 || limit > maximum {
		return from, to, 0, invalid
	}
	return from, to, limit, nil
}
