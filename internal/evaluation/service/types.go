package service

import (
	"encoding/json"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type EvaluationRequest struct {
	Name string
	Spec api.EvaluationRunSpec
}
type EvaluationTemplate struct {
	Spec              api.EvaluationRunSpec
	Sources           map[string]string
	SourcePublication *EvaluationIdentity
}
type EvaluationCheck struct {
	ID     string
	Passed bool
	Reason string
}
type EvaluationLiveTarget struct {
	Component    string                          `json:"component"`
	Resource     api.EvaluationResourceReference `json:"resource"`
	LiveImageIDs []string                        `json:"liveImageIDs"`
	Ready        bool                            `json:"ready"`
}
type EvaluationPreflight struct {
	Checks     []EvaluationCheck
	LiveTarget []EvaluationLiveTarget
}
type EvaluationIdentity struct {
	Name string
	UID  string
}
type EvaluationReleaseRequest struct {
	UID    string
	Reason string
}
type EvaluationView struct {
	Name          string
	UID           string
	Spec          api.EvaluationRunSpec
	Conditions    []metav1.Condition
	CurrentStage  string
	StartedAt     *metav1.Time
	FinishedAt    *metav1.Time
	Phase         string
	PublicationID string
	LiveTarget    []EvaluationLiveTarget
}
type ActiveEvaluation struct {
	Name        string
	UID         string
	StartedAt   *metav1.Time
	StageIndex  int
	StagesTotal int
	Stage       *ActiveEvaluationStage
}
type ActiveEvaluationStage struct {
	ID             string
	StartedAt      *metav1.Time
	GoAt           *metav1.Time
	PlannedSeconds float64
	Injections     []ActiveEvaluationInjection
}
type ActiveEvaluationInjection struct {
	ID            string
	Action        string
	OffsetSeconds float64
	Outcome       string
	NodeName      string
	PodName       string
}
type LoadScenarioRequest struct {
	Name string
	Spec api.LoadScenarioSpec
}
type LoadScenarioView struct {
	Name      string
	UID       string
	Spec      api.LoadScenarioSpec
	InUse     []string
	CreatedAt metav1.Time
}
type LiveWorkload struct {
	UID        string
	Generation int64
}
type LoadScenarioPreflight struct {
	Checks        []EvaluationCheck
	LiveWorkloads map[string]LiveWorkload
}
type EvaluationInput struct {
	Store string
	Key   string
	Size  int64
	Kind  string
	Set   string
	Name  string
}

type PublicationDefinition struct {
	ID         string
	Definition json.RawMessage
}
