package httpapi

import (
	api "github.com/lab-paper-code/chill/api/v1alpha1"
)

type EvaluationRequest struct {
	Name string                `json:"name"`
	Spec api.EvaluationRunSpec `json:"spec"`
}
type EvaluationTemplate struct {
	Spec    api.EvaluationRunSpec `json:"spec"`
	Sources map[string]string     `json:"sources"`
}
type EvaluationCheck struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason"`
}
type EvaluationLiveTarget struct {
	Component    string                          `json:"component"`
	Resource     api.EvaluationResourceReference `json:"resource"`
	SpecImage    string                          `json:"specImage"`
	LiveImageIDs []string                        `json:"liveImageIDs"`
	Match        bool                            `json:"match"`
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
	Name          string                  `json:"name"`
	UID           string                  `json:"uid"`
	Spec          api.EvaluationRunSpec   `json:"spec"`
	Status        api.EvaluationRunStatus `json:"status"`
	Phase         string                  `json:"phase"`
	PublicationID string                  `json:"publicationID,omitempty"`
}
type EvaluationList struct {
	Items []EvaluationView `json:"items"`
}
