package httpapi

import (
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

// Summary deliberately does not embed scheduler inputs or evidence bodies.
type schedulerSummary struct {
	At        time.Time                   `json:"at"`
	Nodes     []schedulerSummaryNode      `json:"nodes"`
	Instances []schedulerSummaryInstance  `json:"instances"`
	Routing   map[string]map[string]int32 `json:"routing"`
	Decision  schedulerSummaryDecision    `json:"decision"`
	Actions   []schedulerSummaryAction    `json:"actions"`
}
type schedulerSummaryNode struct {
	Node        serving.NodeReference `json:"node"`
	PowerMode   string                `json:"powerMode"`
	Ready       bool                  `json:"ready"`
	Measurement bool                  `json:"measurement"`
	Reasons     []string              `json:"reasons"`
}
type schedulerSummaryInstance struct {
	ID               string                `json:"id"`
	TargetID         string                `json:"targetID,omitempty"`
	Node             serving.NodeReference `json:"node"`
	OwnerWorkloadUID string                `json:"ownerWorkloadUID,omitempty"`
	WorkloadUID      string                `json:"workloadUID"`
	ProfileID        string                `json:"profileID"`
	Ready            bool                  `json:"ready"`
	Routable         map[string]bool       `json:"routable"`
	Reasons          []string              `json:"reasons"`
}
type schedulerSummaryDecision struct {
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	Status   string    `json:"status"`
	Reasons  []string  `json:"reasons"`
	TargetID string    `json:"targetID"`
}
type schedulerSummaryAction struct {
	ID              string                        `json:"id"`
	Kind            string                        `json:"kind"`
	Status          string                        `json:"status"`
	Node            serving.NodeReference         `json:"node"`
	Instance        string                        `json:"instance,omitempty"`
	DependsOn       []string                      `json:"dependsOn"`
	Elapsed         time.Duration                 `json:"elapsedNanos"`
	ExpectedSeconds *float64                      `json:"expectedSeconds"`
	ExpectedSource  *serving.ActionExpectedSource `json:"expectedSource"`
}

func summarizeScheduler(state *servingkube.SchedulerControllerState) schedulerSummary {
	result := schedulerSummary{
		At:        state.Snapshot.At,
		Nodes:     make([]schedulerSummaryNode, 0, len(state.Snapshot.Nodes)),
		Instances: make([]schedulerSummaryInstance, 0, len(state.Snapshot.Instances)),
		Routing:   state.Snapshot.Routing,
		Decision:  schedulerSummaryDecision{At: state.Decision.At, Status: state.Decision.Status, Reasons: state.Decision.Reasons},
		Actions:   make([]schedulerSummaryAction, 0),
	}
	if !state.Decision.At.IsZero() {
		result.Decision.ID = state.Decision.At.UTC().Format(time.RFC3339Nano)
	}
	if state.Decision.Target != nil {
		result.Decision.TargetID = state.Decision.Target.ID
	}
	for _, node := range state.Snapshot.Nodes {
		result.Nodes = append(result.Nodes, schedulerSummaryNode{node.Node, node.PowerMode, node.Ready, node.Measurement, node.Reasons})
	}
	for _, instance := range state.Snapshot.Instances {
		profileID := instance.ProfileID
		if profileID == "" && instance.Profile != nil {
			profileID = instance.Profile.ID
		}
		result.Instances = append(result.Instances, schedulerSummaryInstance{instance.ID, instance.TargetID, instance.Node, instance.OwnerWorkloadUID, instance.WorkloadUID, profileID, instance.Ready, instance.Routable, instance.Reasons})
	}
	for _, action := range state.Actions {
		if action.Status != "starting" && action.Status != "running" {
			continue
		}
		result.Actions = append(result.Actions, schedulerSummaryAction{action.ID, action.Kind, action.Status, action.Node, action.Instance, action.DependsOn, action.Elapsed, action.ExpectedSeconds, action.ExpectedSource})
	}
	return result
}
