package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authorizationv1 "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const measurementAuditFailed = "failed"

// MeasurementMutationResult describes native asynchronous admission, not completed work.
type MeasurementMutationResult struct {
	Request         string                        `json:"request"`
	Task            *api.BatchEnergyOptimization  `json:"task"`
	EvidenceDeleted bool                          `json:"evidenceDeleted,omitempty"`
	DeletedEvidence []MeasurementEvidenceLocation `json:"deletedEvidence,omitempty"`
}

// MeasurementEvidenceLocation is the public deletion receipt's logical wire location.
type MeasurementEvidenceLocation struct {
	Store string `json:"store"`
	Key   string `json:"key"`
}

func measurementUnavailable() error {
	return &serving.Error{Code: "MeasurementUnavailable", Message: "measurement service is unavailable"}
}

func (h *Handler) measurements(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	if len(validation.IsDNS1123Label(namespace)) != 0 ||
		(name != "" && len(validation.IsDNS1123Subdomain(name)) != 0) {
		sendError(w, &serving.Error{Code: "InvalidInput", Message: "valid measurement namespace and name are required"})
		return
	}
	verb := map[string]string{http.MethodGet: "list", http.MethodPost: "create", http.MethodDelete: "delete"}[r.Method]
	resource := authorizationv1.ResourceAttributes{
		Namespace: namespace, Verb: verb, Group: api.GroupVersion.Group, Version: api.GroupVersion.Version,
		Resource: "batchenergyoptimizations", Name: name,
	}
	principal := servingkube.PrincipalFromContext(r.Context())
	if err := h.authorizer.AuthorizeResource(r.Context(), principal, resource); err != nil {
		sendError(w, err)
		return
	}
	var value any
	var err error
	var audit *serving.SchedulerActionObservation
	status := http.StatusOK
	switch r.Method {
	case http.MethodGet:
		if h.config.ListMeasurements == nil {
			err = measurementUnavailable()
		} else {
			value, err = h.config.ListMeasurements(r.Context(), namespace)
		}
	case http.MethodPost:
		value, audit, err = h.submitMeasurement(w, r)
		if r.URL.Query().Get("dryRun") == "" {
			status = http.StatusAccepted
		}
	case http.MethodDelete:
		value, audit, err = h.deleteMeasurement(r)
		status = http.StatusAccepted
	}
	if audit != nil {
		err = h.finishMeasurementAudit(audit, value, err)
	}
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, status, value)
}

func (h *Handler) submitMeasurement(
	w http.ResponseWriter, r *http.Request,
) (MeasurementMutationResult, *serving.SchedulerActionObservation, error) {
	var task api.BatchEnergyOptimization
	if err := h.decode(w, r, &task); err != nil {
		return MeasurementMutationResult{}, nil, err
	}
	dryRun := r.URL.Query().Get("dryRun")
	if r.URL.Query().Has("dryRun") && (len(r.URL.Query()["dryRun"]) != 1 || dryRun != meta.DryRunAll) {
		return MeasurementMutationResult{}, nil, &serving.Error{
			Code: "InvalidQuery", Message: "dryRun must be All when supplied", Field: "dryRun",
		}
	}
	namespace := r.PathValue("namespace")
	if task.APIVersion != api.GroupVersion.String() || task.Kind != "BatchEnergyOptimization" ||
		len(validation.IsDNS1123Subdomain(task.Name)) != 0 || len(task.Name) > 200 ||
		(task.Namespace != "" && task.Namespace != namespace) {
		return MeasurementMutationResult{}, nil, &serving.Error{
			Code: "InvalidBody", Message: "native BatchEnergyOptimization identity must match the requested namespace",
		}
	}
	if h.config.SubmitMeasurement == nil {
		return MeasurementMutationResult{}, nil, measurementUnavailable()
	}
	task.Namespace = namespace
	audit, err := h.beginMeasurementAudit(r, task.Name, "", "", dryRun == meta.DryRunAll, &task.Spec)
	if err != nil {
		return MeasurementMutationResult{}, nil, err
	}
	value, err := h.config.SubmitMeasurement(r.Context(), task, dryRun == meta.DryRunAll)
	return value, audit, err
}

func (h *Handler) deleteMeasurement(
	r *http.Request,
) (MeasurementMutationResult, *serving.SchedulerActionObservation, error) {
	version, err := precondition(r)
	if err != nil {
		return MeasurementMutationResult{}, nil, err
	}
	uid := r.URL.Query().Get("uid")
	if len(r.URL.Query()["uid"]) != 1 || strings.TrimSpace(uid) == "" || strings.ContainsAny(uid, " \t\r\n") {
		return MeasurementMutationResult{}, nil, &serving.Error{
			Code: "InvalidQuery", Message: "queried native UID is required", Field: "uid",
		}
	}
	if h.config.DeleteMeasurement == nil {
		return MeasurementMutationResult{}, nil, measurementUnavailable()
	}
	name := r.PathValue("name")
	audit, err := h.beginMeasurementAudit(r, name, uid, version, false, nil)
	if err != nil {
		return MeasurementMutationResult{}, nil, err
	}
	value, err := h.config.DeleteMeasurement(r.Context(), r.PathValue("namespace"), name, uid, version)
	return value, audit, err
}

func (h *Handler) finishMeasurementAudit(
	audit *serving.SchedulerActionObservation, value any, mutationErr error,
) error {
	now := time.Now().UTC()
	audit.CompletedAt = &now
	audit.Phase = measurementAuditFailed
	if mutationErr != nil {
		audit.Error = errorCode(mutationErr)
	}
	if result, ok := value.(MeasurementMutationResult); ok && result.Task != nil {
		if mutationErr == nil {
			audit.Phase = result.Request
		}
		audit.Actual, _ = json.Marshal(struct {
			Request         string                          `json:"request"`
			UID             string                          `json:"uid,omitempty"`
			ResourceVersion string                          `json:"resourceVersion,omitempty"`
			EvidenceDeleted bool                            `json:"evidenceDeleted"`
			CleanedObjects  int                             `json:"cleanedObjects"`
			Spec            api.BatchEnergyOptimizationSpec `json:"spec"`
		}{
			result.Request, string(result.Task.UID), result.Task.ResourceVersion,
			result.EvidenceDeleted, len(result.DeletedEvidence), result.Task.Spec,
		})
	}
	if err := h.observationLog.SchedulerRecord(serving.SchedulerObservation{At: now, Action: audit}); err != nil && mutationErr == nil {
		return &serving.Error{
			Code: "MeasurementAuditIncomplete",
			Message: "measurement mutation may have been accepted, but its final audit record could not be saved; " +
				"query the native measurement before retrying",
		}
	}
	return mutationErr
}

func (h *Handler) beginMeasurementAudit(
	r *http.Request, name, uid, version string, dryRun bool, spec *api.BatchEnergyOptimizationSpec,
) (*serving.SchedulerActionObservation, error) {
	if h.observationLog == nil {
		return nil, &serving.Error{Code: "MeasurementAuditUnavailable", Message: "durable measurement audit is unavailable"}
	}
	username, actorUID := servingkube.PrincipalFromContext(r.Context()).Actor()
	expected, err := json.Marshal(struct {
		ActorUsername   string                           `json:"actorUsername"`
		ActorUID        string                           `json:"actorUID"`
		Namespace       string                           `json:"namespace"`
		Name            string                           `json:"name"`
		UID             string                           `json:"uid,omitempty"`
		ResourceVersion string                           `json:"resourceVersion,omitempty"`
		DryRun          bool                             `json:"dryRun"`
		RequestedSpec   *api.BatchEnergyOptimizationSpec `json:"requestedSpec,omitempty"`
	}{username, actorUID, r.PathValue("namespace"), name, uid, version, dryRun, spec})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	action := &serving.SchedulerActionObservation{
		ID:   "measurement/" + r.Method + "/" + r.PathValue("namespace") + "/" + name + "/" + now.Format(time.RFC3339Nano),
		Kind: "measurement-" + strings.ToLower(r.Method), Phase: "validated", StartedAt: now, Expected: expected,
	}
	if err := h.observationLog.SchedulerRecord(serving.SchedulerObservation{At: now, Action: action}); err != nil {
		return nil, err
	}
	return action, nil
}
