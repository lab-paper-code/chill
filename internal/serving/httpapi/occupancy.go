package httpapi

import (
	"net/http"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// OccupancyIntent selects what to measure. Protocol fields are server-owned.
type OccupancyIntent struct {
	Name                    string `json:"name,omitempty"`
	Node                    string `json:"node"`
	BatchEnergyOptimization string `json:"batchEnergyOptimization"`
	Workload                string `json:"workload"`
	Batch                   int32  `json:"batch"`
}

// OccupancyMutationResult returns the complete immutable native task.
type OccupancyMutationResult struct {
	Request         string                            `json:"request"`
	Task            *api.DispatchOccupancyMeasurement `json:"task"`
	EvidenceDeleted bool                              `json:"evidenceDeleted,omitempty"`
	DeletedEvidence []MeasurementEvidenceLocation     `json:"deletedEvidence,omitempty"`
}

func (h *Handler) submitOccupancy(w http.ResponseWriter, r *http.Request) (OccupancyMutationResult, *serving.SchedulerActionObservation, error) {
	var intent OccupancyIntent
	if err := h.decode(w, r, &intent); err != nil {
		return OccupancyMutationResult{}, nil, err
	}
	dryRun := r.URL.Query().Get("dryRun")
	if r.URL.Query().Has("dryRun") && (len(r.URL.Query()["dryRun"]) != 1 || dryRun != meta.DryRunAll) {
		return OccupancyMutationResult{}, nil, &serving.Error{Code: "InvalidQuery", Message: "dryRun must be All when supplied", Field: "dryRun"}
	}
	if (intent.Name != "" && (len(validation.IsDNS1123Subdomain(intent.Name)) != 0 || len(intent.Name) > 200)) ||
		len(validation.IsDNS1123Subdomain(intent.Node)) != 0 ||
		len(validation.IsDNS1123Subdomain(intent.BatchEnergyOptimization)) != 0 ||
		len(validation.IsDNS1123Subdomain(intent.Workload)) != 0 || intent.Batch < 1 {
		return OccupancyMutationResult{}, nil, &serving.Error{Code: "InvalidBody", Message: "valid node, BEO, Workload and positive batch are required"}
	}
	if h.config.BuildOccupancy == nil || h.config.SubmitOccupancy == nil {
		return OccupancyMutationResult{}, nil, &serving.Error{Code: "OccupancyPolicyUnavailable", Message: "occupancy service is unavailable"}
	}
	task, err := h.config.BuildOccupancy(r.Context(), r.PathValue("namespace"), intent)
	if err != nil {
		return OccupancyMutationResult{}, nil, err
	}
	audit, err := h.beginMeasurementAudit(r, task.Name, "", "", dryRun == meta.DryRunAll, &task.Spec)
	if err != nil {
		return OccupancyMutationResult{}, nil, err
	}
	value, err := h.config.SubmitOccupancy(r.Context(), *task, dryRun == meta.DryRunAll)
	return value, audit, err
}

func (h *Handler) deleteOccupancy(r *http.Request) (OccupancyMutationResult, *serving.SchedulerActionObservation, error) {
	version, err := precondition(r)
	if err != nil {
		return OccupancyMutationResult{}, nil, err
	}
	uid := r.URL.Query().Get("uid")
	if len(r.URL.Query()["uid"]) != 1 || strings.TrimSpace(uid) == "" || strings.ContainsAny(uid, " \t\r\n") {
		return OccupancyMutationResult{}, nil, &serving.Error{Code: "InvalidQuery", Message: "queried native UID is required", Field: "uid"}
	}
	if h.config.DeleteOccupancy == nil {
		return OccupancyMutationResult{}, nil, measurementUnavailable()
	}
	audit, err := h.beginMeasurementAudit(r, r.PathValue("name"), uid, version, false, nil)
	if err != nil {
		return OccupancyMutationResult{}, nil, err
	}
	value, err := h.config.DeleteOccupancy(r.Context(), r.PathValue("namespace"), r.PathValue("name"), uid, version)
	return value, audit, err
}
