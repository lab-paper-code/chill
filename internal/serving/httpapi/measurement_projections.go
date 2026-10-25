package httpapi

import (
	"context"
	"net/http"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authorizationv1 "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const measurementGetVerb = "get"

// MeasurementProjection exposes only safe measurement conditions and saved evidence identities.
type MeasurementProjection struct {
	Kind       string                          `json:"kind"`
	Metadata   MeasurementProjectionMetadata   `json:"metadata"`
	Node       string                          `json:"node"`
	Conditions any                             `json:"conditions"`
	Status     any                             `json:"status"`
	References MeasurementProjectionReferences `json:"references"`
}
type MeasurementProjectionMetadata struct {
	Namespace         string    `json:"namespace"`
	Name              string    `json:"name"`
	UID               string    `json:"uid"`
	ResourceVersion   string    `json:"resourceVersion"`
	CreationTimestamp meta.Time `json:"creationTimestamp"`
}
type MeasurementProjectionReferences struct {
	CR       MeasurementCRReference        `json:"cr"`
	Evidence []MeasurementEvidenceLocation `json:"evidence"`
}
type MeasurementCRReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}
type MeasurementProjectionList struct {
	Items []MeasurementProjection `json:"items"`
}
type operationConditions struct {
	Steps []operationCondition `json:"steps"`
}
type operationCondition struct {
	Name                    string                    `json:"name"`
	Kind                    api.OperationCostStepKind `json:"kind"`
	PowerMode               string                    `json:"powerMode,omitempty"`
	BatchEnergyOptimization string                    `json:"batchEnergyOptimization,omitempty"`
	IdleDurationSeconds     *int32                    `json:"idleDurationSeconds,omitempty"`
}
type operationStatus struct {
	Phase  api.MeasurementPhase  `json:"phase,omitempty"`
	Reason string                `json:"reason,omitempty"`
	Steps  []operationStepStatus `json:"steps,omitempty"`
}
type operationStepStatus struct {
	Name    string `json:"name"`
	Key     string `json:"key"`
	Verdict string `json:"verdict"`
}
type occupancyConditions struct {
	Workload                string `json:"workload"`
	BatchEnergyOptimization string `json:"batchEnergyOptimization"`
	Batch                   int32  `json:"batch"`
	SupplyMode              string `json:"supplyMode,omitempty"`
	WarmupSeconds           int32  `json:"warmupSeconds"`
	DurationSeconds         int32  `json:"durationSeconds"`
	Workers                 int32  `json:"workers"`
}
type occupancyStatus struct {
	Phase           api.MeasurementPhase         `json:"phase,omitempty"`
	Reason          string                       `json:"reason,omitempty"`
	PowerMode       string                       `json:"powerMode,omitempty"`
	PerRequestNanos *int64                       `json:"perRequestNanos,omitempty"`
	Throughput      string                       `json:"throughput,omitempty"`
	Result          *MeasurementEvidenceLocation `json:"result,omitempty"`
}

func measurementProjection(kind, node string, m meta.ObjectMeta) MeasurementProjection {
	return MeasurementProjection{Kind: kind, Node: node,
		Metadata:   MeasurementProjectionMetadata{m.Namespace, m.Name, string(m.UID), m.ResourceVersion, m.CreationTimestamp},
		References: MeasurementProjectionReferences{CR: MeasurementCRReference{api.GroupVersion.String(), kind, m.Namespace, m.Name, string(m.UID)}, Evidence: []MeasurementEvidenceLocation{}},
	}
}

// ProjectOperationCostMeasurement retains only recorded step keys; it never invents evidence.
func ProjectOperationCostMeasurement(cr api.OperationCostMeasurement, store string) MeasurementProjection {
	p := measurementProjection("OperationCostMeasurement", cr.Spec.Node, cr.ObjectMeta)
	conditions := operationConditions{Steps: make([]operationCondition, len(cr.Spec.Steps))}
	for i, s := range cr.Spec.Steps {
		conditions.Steps[i] = operationCondition{s.Name, s.Kind, s.PowerMode, s.BatchEnergyOptimization, s.IdleDurationSeconds}
	}
	status := operationStatus{Phase: cr.Status.Phase, Reason: cr.Status.Reason}
	for _, s := range cr.Status.Steps {
		status.Steps = append(status.Steps, operationStepStatus{s.Name, s.Key, s.Verdict})
		if s.Key != "" {
			p.References.Evidence = append(p.References.Evidence, MeasurementEvidenceLocation{Store: store, Key: s.Key})
		}
	}
	p.Conditions, p.Status = conditions, status
	return p
}

// ProjectDispatchOccupancyMeasurement preserves absent results and unspecified supply mode.
func ProjectDispatchOccupancyMeasurement(cr api.DispatchOccupancyMeasurement) MeasurementProjection {
	p := measurementProjection("DispatchOccupancyMeasurement", cr.Spec.Node, cr.ObjectMeta)
	s := cr.Spec
	p.Conditions = occupancyConditions{s.Workload, s.BatchEnergyOptimization, s.Batch, s.SupplyMode, s.WarmupSeconds, s.DurationSeconds, s.Workers}
	status := occupancyStatus{Phase: cr.Status.Phase, Reason: cr.Status.Reason, PowerMode: cr.Status.PowerMode, PerRequestNanos: cr.Status.PerRequestNanos, Throughput: cr.Status.Throughput}
	if cr.Status.Result != nil {
		status.Result = &MeasurementEvidenceLocation{Store: cr.Status.Result.Store, Key: cr.Status.Result.Key}
		if status.Result.Key != "" {
			p.References.Evidence = append(p.References.Evidence, *status.Result)
		}
	}
	p.Status = status
	return p
}
func (h *Handler) operationCostMeasurements(w http.ResponseWriter, r *http.Request) {
	h.measurementProjectionRead(w, r, "operationcostmeasurements", h.config.ListOperationCostMeasurements, h.config.GetOperationCostMeasurement)
}
func (h *Handler) dispatchOccupancyMeasurements(w http.ResponseWriter, r *http.Request) {
	h.measurementProjectionRead(w, r, "dispatchoccupancymeasurements", h.config.ListDispatchOccupancyMeasurements, h.config.GetDispatchOccupancyMeasurement)
}
func (h *Handler) measurementProjectionRead(w http.ResponseWriter, r *http.Request, resource string,
	list func(context.Context, string) (MeasurementProjectionList, error), get func(context.Context, string, string) (MeasurementProjection, error)) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	if len(validation.IsDNS1123Label(namespace)) != 0 || (name != "" && len(validation.IsDNS1123Subdomain(name)) != 0) {
		sendError(w, &serving.Error{Code: "InvalidInput", Message: "valid measurement namespace and name are required"})
		return
	}
	verb := "list"
	if name != "" {
		verb = measurementGetVerb
	}
	if err := h.authorizer.AuthorizeResource(r.Context(), servingkube.PrincipalFromContext(r.Context()), authorizationv1.ResourceAttributes{Namespace: namespace, Name: name, Verb: verb, Group: api.GroupVersion.Group, Version: api.GroupVersion.Version, Resource: resource}); err != nil {
		sendError(w, err)
		return
	}
	var value any
	var err error
	if name == "" {
		if list == nil {
			err = measurementUnavailable()
		} else {
			value, err = list(r.Context(), namespace)
		}
	} else {
		if get == nil {
			err = measurementUnavailable()
		} else {
			value, err = get(r.Context(), namespace, name)
		}
	}
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, http.StatusOK, value)
}
