package queryapi

import (
	"errors"
	"strings"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// BatchEnergyOptimizationPreflightSnapshot pairs one campaign a caller intends to
// apply with the outcome of resolving the plan it selects. A request that does
// not resolve is the answer rather than a failed read, so the reason travels
// beside the plan instead of replacing it.
type BatchEnergyOptimizationPreflightSnapshot struct {
	ReadAt   time.Time
	Campaign edgev1alpha1.BatchEnergyOptimization
	Reason   string
	// SessionPlan is what PrepareSession resolved, nil when it did not
	// resolve (Reason says why).
	SessionPlan *SessionPreflightPlan
}

// SessionPreflightPlan is the part of a resolved measurement session a
// requester decides on. No duration is predicted: levels end on service
// record counts, so their length depends on the device's throughput.
type SessionPreflightPlan struct {
	PlannedBatchSizes []int `json:"plannedBatchSizes"`
	// MinimumSamples is the service record count N each level ends on, the
	// fewest that estimate the Workload's SLO percentile.
	MinimumSamples int     `json:"minimumSamples"`
	SLOPercentile  float64 `json:"sloPercentile"`
}

type MeasurementBatchPlanReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type MeasurementBatchPlan struct {
	ConditionMatch     string                       `json:"conditionMatch"`
	PlannedBatches     []int                        `json:"plannedBatches"`
	MeasuredBatches    []int                        `json:"measuredBatches"`
	InProgressBatches  []int                        `json:"inProgressBatches"`
	MissingBatches     []int                        `json:"missingBatches"`
	SearchMayStopEarly bool                         `json:"searchMayStopEarly"`
	Reasons            []MeasurementBatchPlanReason `json:"reasons"`
}

const BatchEnergyOptimizationPreflightAPIVersion = "console.chill.dacs.io/v1alpha1"

// BatchEnergyOptimizationPreflight is the bounded read model for one campaign that has
// not been applied.
type BatchEnergyOptimizationPreflight struct {
	APIVersion string                                   `json:"apiVersion"`
	Kind       string                                   `json:"kind"`
	ReadAt     time.Time                                `json:"readAt"`
	Campaign   BatchEnergyOptimizationPreflightCampaign `json:"campaign"`
	Resolves   bool                                     `json:"resolves"`
	Reason     string                                   `json:"reason,omitempty"`
	BatchPlan  MeasurementBatchPlan                     `json:"batchPlan"`
	// Session is present when the request resolves.
	Session *SessionPreflightPlan `json:"session,omitempty"`
}

type BatchEnergyOptimizationPreflightCampaign struct {
	Namespace string                                   `json:"namespace"`
	Name      string                                   `json:"name"`
	Spec      edgev1alpha1.BatchEnergyOptimizationSpec `json:"spec"`
}

// BuildBatchEnergyOptimizationPreflight answers whether a request resolves,
// with the batch sizes the session may measure. A session measures on its own
// Node, so no catalog or request overlap is reported.
func BuildBatchEnergyOptimizationPreflight(snapshot BatchEnergyOptimizationPreflightSnapshot) (BatchEnergyOptimizationPreflight, error) {
	resolved := snapshot.SessionPlan != nil
	switch {
	case resolved && strings.TrimSpace(snapshot.Reason) != "":
		return BatchEnergyOptimizationPreflight{}, errors.New("a resolved preflight carries no reason")
	case !resolved && strings.TrimSpace(snapshot.Reason) == "":
		return BatchEnergyOptimizationPreflight{}, errors.New("an unresolved preflight requires a reason")
	}
	plan := MeasurementBatchPlan{
		ConditionMatch: "unknown", PlannedBatches: []int{}, MeasuredBatches: []int{}, InProgressBatches: []int{},
		MissingBatches: []int{}, Reasons: []MeasurementBatchPlanReason{},
	}
	if resolved {
		plan.ConditionMatch, plan.SearchMayStopEarly = "planned-only", true
		plan.PlannedBatches = append(plan.PlannedBatches, snapshot.SessionPlan.PlannedBatchSizes...)
		plan.MissingBatches = append(plan.MissingBatches, snapshot.SessionPlan.PlannedBatchSizes...)
	} else {
		plan.Reasons = append(plan.Reasons, MeasurementBatchPlanReason{"PlanUnresolved", snapshot.Reason})
	}
	campaign := snapshot.Campaign.DeepCopy()
	return BatchEnergyOptimizationPreflight{
		APIVersion: BatchEnergyOptimizationPreflightAPIVersion, Kind: "BatchEnergyOptimizationPreflight",
		ReadAt: snapshot.ReadAt.UTC(),
		Campaign: BatchEnergyOptimizationPreflightCampaign{
			Namespace: campaign.Namespace, Name: campaign.Name, Spec: campaign.Spec,
		},
		Resolves: resolved, Reason: snapshot.Reason, BatchPlan: plan, Session: snapshot.SessionPlan,
	}, nil
}
