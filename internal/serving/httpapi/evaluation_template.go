package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/application"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

func (h *Handler) evaluationTemplate(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	value, err := h.config.GetEvaluationTemplate(r.Context())
	if err != nil {
		sendError(w, err)
		return
	}
	_, config, err := application.SchedulerConfigMap(
		r.Context(), h.authorizer.Client, h.config.SchedulerNamespace,
		h.config.SchedulerConfigMap, h.config.SchedulerConfigKey,
	)
	if err != nil {
		sendError(w, err)
		return
	}
	var policy struct {
		Sampling struct {
			Interval time.Duration `json:"Interval"`
		} `json:"sampling"`
	}
	if err := json.Unmarshal(config["powerObservation"], &policy); err != nil {
		sendError(w, err)
		return
	}
	value.Spec.PowerMeasurement = api.PowerMeasurement{
		Sampling: api.PowerSampling{IntervalNanos: int64(policy.Sampling.Interval)},
	}
	nodes := map[string]bool{}
	if h.config.SchedulerMeters != nil {
		for _, meter := range h.config.SchedulerMeters() {
			source, err := json.Marshal(meter.Source)
			if err != nil {
				sendError(w, err)
				return
			}
			projected := api.EvaluationMeter{ID: meter.ID, SourceIdentity: string(source)}
			value.Spec.PowerMeasurement.Meters = append(value.Spec.PowerMeasurement.Meters, projected)
			value.Spec.PowerMeasurement.Series = meter.Source.Metric
			if !nodes[meter.Node.UID] {
				nodes[meter.Node.UID] = true
				node := api.EvaluationObjectReference{Name: meter.Node.Name, UID: types.UID(meter.Node.UID)}
				value.Spec.PowerMeasurement.Nodes = append(value.Spec.PowerMeasurement.Nodes, node)
			}
		}
	}
	value.Sources["powerMeasurement"] = "config"
	sendJSON(w, http.StatusOK, value)
}
