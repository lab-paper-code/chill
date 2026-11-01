package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

type observationEvent struct {
	Type string `json:"type"`
	At time.Time `json:"at"`
	live.Event
}

func (h *Handler) observationEvents(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	at, parseErr := time.Parse(time.RFC3339Nano, query.Get("at"))
	if err != nil || parseErr != nil || at.IsZero() || len(query) != 1 || len(query["at"]) != 1 {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "one RFC3339Nano event timestamp is required"})
		return
	}
	records, err := h.observationLog.EventObservations(r.Context(), at, h.config.MaxQueryEvents)
	if err != nil {
		sendError(w, err)
		return
	}
	events := make([]observationEvent, 0, len(records))
	for _, record := range records {
		event := observationEvent{Type: "event", At: record.At}
		if action := record.Action; action != nil {
			var operation servingkube.Operation
			if len(action.Expected) != 0 {
				if err := json.Unmarshal(action.Expected, &operation); err != nil {
					sendError(w, err)
					return
				}
			}
			uid := operation.WorkloadUID
			if uid == "" && operation.Prepare != nil && operation.Prepare.Owner != nil {
				uid = operation.Prepare.Owner.UID
			}
			projected := &live.ActionEvent{Action: live.Action{
				ID: action.ID, Kind: action.Kind, Node: live.NodeRef{UID: action.Node.UID, Name: action.Node.Name},
				WorkloadUID: uid, Status: action.Phase, StartedAt: action.StartedAt.UTC().Format(time.RFC3339Nano), ExpectedSeconds: action.ExpectedSeconds,
			}, Phase: "started"}
			if source := action.ExpectedSource; source != nil {
				reference, err := json.Marshal(source.Reference)
				if err != nil {
					sendError(w, err)
					return
				}
				projected.ExpectedSource = &live.ExpectedSource{CostID: source.CostID, Reference: reference, Statistic: source.Statistic, TrialCount: source.TrialCount}
			}
			if action.CompletedAt != nil {
				completed := action.CompletedAt.UTC().Format(time.RFC3339Nano)
				projected.CompletedAt = &completed
				projected.Phase = "completed"
				if action.Phase != "complete" {
					projected.Phase = "failed"
				}
				projected.Result = &live.ActionResult{Status: action.Phase, Error: action.Error}
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
	sendJSON(w, http.StatusOK, struct { Events []observationEvent `json:"events"` }{events})
}
