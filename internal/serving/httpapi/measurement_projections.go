package httpapi

import (
	"encoding/json"

	"github.com/lab-paper-code/chill/internal/serving/measurement"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	measurementGetVerb  = "get"
	measurementListVerb = "list"
)

// MeasurementProjection exposes only safe measurement conditions and saved evidence identities.
type MeasurementProjection struct {
	Kind       string                          `json:"kind"`
	Metadata   MeasurementProjectionMetadata   `json:"metadata"`
	Node       string                          `json:"node"`
	Conditions any                             `json:"conditions"`
	Status     any                             `json:"status"`
	References MeasurementProjectionReferences `json:"references"`
	Assessment json.RawMessage                 `json:"assessment"`
}
type MeasurementProjectionMetadata struct {
	Namespace         string    `json:"namespace"`
	Name              string    `json:"name"`
	UID               string    `json:"uid"`
	ResourceVersion   string    `json:"resourceVersion"`
	CreationTimestamp meta.Time `json:"creationTimestamp"`
}
type MeasurementProjectionReferences struct {
	CR       MeasurementCRReference                    `json:"cr"`
	Evidence []measurement.MeasurementEvidenceLocation `json:"evidence"`
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
	Name          string   `json:"name"`
	Key           string   `json:"key"`
	Verdict       string   `json:"verdict"`
	OperationID   string   `json:"operationID"`
	DurationNanos *int64   `json:"durationNanos"`
	WallJoules    *float64 `json:"wallJoules"`
	Reason        string   `json:"reason"`
	FromPowerMode string   `json:"fromPowerMode,omitempty"`
	ToPowerMode   string   `json:"toPowerMode,omitempty"`
	Model         string   `json:"model,omitempty"`
}

func measurementProjection(kind, node string, m meta.ObjectMeta) MeasurementProjection {
	return MeasurementProjection{Kind: kind, Node: node,
		Metadata:   MeasurementProjectionMetadata{m.Namespace, m.Name, string(m.UID), m.ResourceVersion, m.CreationTimestamp},
		References: MeasurementProjectionReferences{CR: MeasurementCRReference{api.GroupVersion.String(), kind, m.Namespace, m.Name, string(m.UID)}, Evidence: []measurement.MeasurementEvidenceLocation{}},
	}
}

// ProjectOperationCostMeasurement retains only recorded step keys; it never invents evidence.
func ProjectOperationCostMeasurement(cr api.OperationCostMeasurement, store string, steps []servingkube.OperationStepProjection) MeasurementProjection {
	p := measurementProjection("OperationCostMeasurement", cr.Spec.Node, cr.ObjectMeta)
	conditions := operationConditions{Steps: make([]operationCondition, len(cr.Spec.Steps))}
	for i, s := range cr.Spec.Steps {
		conditions.Steps[i] = operationCondition{s.Name, s.Kind, s.PowerMode, s.BatchEnergyOptimization, s.IdleDurationSeconds}
	}
	status := operationStatus{Phase: cr.Status.Phase, Reason: cr.Status.Reason}
	for i, s := range cr.Status.Steps {
		step := steps[i]
		verdict := s.Verdict
		if step.Verdict != "" {
			verdict = step.Verdict
		}
		status.Steps = append(status.Steps, operationStepStatus{
			Name: s.Name, Key: s.Key, Verdict: verdict, OperationID: step.OperationID,
			DurationNanos: step.DurationNanos, WallJoules: step.WallJoules, Reason: step.Reason,
			FromPowerMode: step.FromPowerMode, ToPowerMode: step.ToPowerMode, Model: step.Model,
		})
		if s.Key != "" {
			p.References.Evidence = append(p.References.Evidence, measurement.MeasurementEvidenceLocation{Store: store, Key: s.Key})
		}
	}
	p.Conditions, p.Status = conditions, status
	return p
}
