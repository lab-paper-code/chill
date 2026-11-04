package httpapi

import (
	api "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type EvaluationRequest struct {
	Name string                `json:"name"`
	Spec api.EvaluationRunSpec `json:"spec"`
}
type EvaluationTemplate struct {
	Spec              api.EvaluationRunSpec `json:"spec"`
	Sources           map[string]string     `json:"sources"`
	SourcePublication *EvaluationIdentity   `json:"sourcePublication,omitempty"`
}
type EvaluationCheck struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason"`
}
type EvaluationLiveTarget struct {
	Component    string                          `json:"component"`
	Resource     api.EvaluationResourceReference `json:"resource"`
	LiveImageIDs []string                        `json:"liveImageIDs"`
	Ready        bool                            `json:"ready"`
}
type EvaluationPreflight struct {
	Checks     []EvaluationCheck      `json:"checks"`
	LiveTarget []EvaluationLiveTarget `json:"liveTarget"`
}
type EvaluationIdentity struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}
type EvaluationReleaseRequest struct {
	UID    string `json:"uid"`
	Reason string `json:"reason"`
}
type EvaluationView struct {
	Name             string                 `json:"name"`
	UID              string                 `json:"uid"`
	Spec             api.EvaluationRunSpec  `json:"spec"`
	Conditions       []metav1.Condition     `json:"conditions"`
	CurrentStage     string                 `json:"currentStage,omitempty"`
	CurrentProcedure string                 `json:"currentProcedure,omitempty"`
	Procedures       []EvaluationProcedure  `json:"procedures"`
	Stages           []EvaluationStage      `json:"stages"`
	StartedAt        *metav1.Time           `json:"startedAt,omitempty"`
	FinishedAt       *metav1.Time           `json:"finishedAt,omitempty"`
	Phase            string                 `json:"phase"`
	PublicationID    string                 `json:"publicationID,omitempty"`
	LiveTarget       []EvaluationLiveTarget `json:"liveTarget"`
}
type EvaluationProcedure struct {
	Step               string                 `json:"step"`
	StartedAt          *metav1.Time           `json:"startedAt"`
	FinishedAt         *metav1.Time           `json:"finishedAt"`
	Reason             string                 `json:"reason,omitempty"`
	MeasurementVerdict api.MeasurementVerdict `json:"measurementVerdict,omitempty"`
}
type EvaluationStage struct {
	ID                 string                       `json:"id"`
	Name               string                       `json:"name"`
	UID                string                       `json:"uid"`
	Conditions         []metav1.Condition           `json:"conditions"`
	MeasurementVerdict api.MeasurementVerdict       `json:"measurementVerdict,omitempty"`
	CurrentProcedure   string                       `json:"currentProcedure,omitempty"`
	Procedures         []EvaluationProcedure        `json:"procedures"`
	StartedAt          *metav1.Time                 `json:"startedAt"`
	FinishedAt         *metav1.Time                 `json:"finishedAt"`
	GoAt               *metav1.Time                 `json:"goAt"`
	ProvisionalD1      *api.EvaluationProvisionalD1 `json:"provisionalD1,omitempty"`
	// Always [] from this server (capability marker); absent only from older servers.
	Progress       []api.EvaluationStageProgress `json:"progress"`
	LastProgressAt string                        `json:"lastProgressAt,omitempty"`
	LoadJobs       []EvaluationLoadJob           `json:"loadJobs"`
}
type EvaluationLoadJob struct {
	Name        string       `json:"name"`
	UID         string       `json:"uid"`
	Status      string       `json:"status"`
	StartedAt   *metav1.Time `json:"startedAt"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	Reason      string       `json:"reason,omitempty"`
}
type EvaluationList struct {
	Items []EvaluationView `json:"items"`
}
