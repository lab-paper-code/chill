package operatorconsole

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"slices"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// BatchEnergyOptimizationPreflightSnapshot pairs one campaign a caller intends to
// apply with the outcome of resolving the plan it selects. A request that does
// not resolve is the answer rather than a failed read, so the reason travels
// beside the plan instead of replacing it.
type BatchEnergyOptimizationPreflightSnapshot struct {
	ReadAt   time.Time
	Campaign edgev1alpha1.BatchEnergyOptimization
	Plan     profilederivation.CurvePlan
	Reason   string
	VariantID string
	Requests []BatchEnergyOptimizationPreflightRequest
	RequestsComplete bool
	ExactEvidence []MeasurementPreflightEvidence
}

type BatchEnergyOptimizationPreflightRequest struct {
	Campaign edgev1alpha1.BatchEnergyOptimization
	Plan profilederivation.CurvePlan
}

// MeasurementPreflightEvidence comes from the verified catalog or a UID-bound frozen plan.
type MeasurementPreflightEvidence struct {
	Condition profilederivation.CurvePlanSpec
	Batches []int
	InProgress bool
}

type MeasurementBatchPlanReason struct {
	Code string `json:"code"`
	Message string `json:"message"`
}

type MeasurementBatchPlan struct {
	VariantID *string `json:"variantId"`
	ConditionMatch string `json:"conditionMatch"`
	RequestedBatches []int `json:"requestedBatches"`
	RequestableBatches []int `json:"requestableBatches"`
	PlannedBatches []int `json:"plannedBatches"`
	MeasuredBatches []int `json:"measuredBatches"`
	InProgressBatches []int `json:"inProgressBatches"`
	MissingBatches []int `json:"missingBatches"`
	SubsetAllowed bool `json:"subsetAllowed"`
	SearchMayStopEarly bool `json:"searchMayStopEarly"`
	Reasons []MeasurementBatchPlanReason `json:"reasons"`
}

const BatchEnergyOptimizationPreflightAPIVersion = "console.chill.dacs.io/v1alpha1"

// BatchEnergyOptimizationPreflight is the bounded read model for one campaign that has
// not been applied. The plan is republished as the canonical JSON the campaign
// would have recorded, so what a preflight shows and what a campaign records
// cannot drift apart.
type BatchEnergyOptimizationPreflight struct {
	APIVersion string                                   `json:"apiVersion"`
	Kind       string                                   `json:"kind"`
	ReadAt     time.Time                                `json:"readAt"`
	Campaign   BatchEnergyOptimizationPreflightCampaign `json:"campaign"`
	Resolves   bool                                     `json:"resolves"`
	Reason     string                                   `json:"reason,omitempty"`
	Plan       json.RawMessage                          `json:"plan,omitempty"`
	BatchPlan MeasurementBatchPlan `json:"batchPlan"`
}

type BatchEnergyOptimizationPreflightCampaign struct {
	Namespace string                                   `json:"namespace"`
	Name      string                                   `json:"name"`
	Spec      edgev1alpha1.BatchEnergyOptimizationSpec `json:"spec"`
}

func BuildBatchEnergyOptimizationPreflight(
	snapshot BatchEnergyOptimizationPreflightSnapshot,
) (BatchEnergyOptimizationPreflight, error) {
	resolved := snapshot.Plan.SchemaVersion() != ""
	switch {
	case resolved && strings.TrimSpace(snapshot.Reason) != "":
		return BatchEnergyOptimizationPreflight{}, errors.New("a resolved preflight carries no reason")
	case !resolved && strings.TrimSpace(snapshot.Reason) == "":
		return BatchEnergyOptimizationPreflight{}, errors.New("an unresolved preflight requires a reason")
	}

	var plan json.RawMessage
	if resolved {
		canonical, err := snapshot.Plan.CanonicalJSON()
		if err != nil {
			return BatchEnergyOptimizationPreflight{}, fmt.Errorf("encode batch-energy-optimization preflight plan: %w", err)
		}
		plan = canonical
	}

	campaign := snapshot.Campaign.DeepCopy()
	return BatchEnergyOptimizationPreflight{
		APIVersion: BatchEnergyOptimizationPreflightAPIVersion,
		Kind:       "BatchEnergyOptimizationPreflight",
		ReadAt:     snapshot.ReadAt.UTC(),
		Campaign: BatchEnergyOptimizationPreflightCampaign{
			Namespace: campaign.Namespace,
			Name:      campaign.Name,
			Spec:      campaign.Spec,
		},
		Resolves: resolved,
		Reason:   snapshot.Reason,
		Plan:     plan,
		BatchPlan: BuildMeasurementBatchPlan(snapshot),
	}, nil
}

// BuildMeasurementBatchPlan never treats a projected request as immutable evidence.
func BuildMeasurementBatchPlan(snapshot BatchEnergyOptimizationPreflightSnapshot) MeasurementBatchPlan {
	result := MeasurementBatchPlan{
		ConditionMatch: "unknown", RequestedBatches: []int{}, RequestableBatches: []int{},
		PlannedBatches: []int{}, MeasuredBatches: []int{}, InProgressBatches: []int{},
		MissingBatches: []int{}, Reasons: []MeasurementBatchPlanReason{},
	}
	for _, batch := range snapshot.Campaign.Spec.BatchSizes {
		result.RequestedBatches = append(result.RequestedBatches, int(batch))
	}
	if snapshot.Plan.SchemaVersion() == "" {
		result.Reasons = append(result.Reasons, MeasurementBatchPlanReason{"PlanUnresolved", snapshot.Reason})
		return result
	}
	result.ConditionMatch = "planned-only"
	result.PlannedBatches = snapshot.Plan.PlannedBatchSizes()
	result.RequestableBatches = slices.Clone(result.PlannedBatches)
	result.SubsetAllowed = len(snapshot.Campaign.Spec.BatchSizes) != 0
	result.SearchMayStopEarly = len(result.PlannedBatches) > 1
	if !result.SubsetAllowed {
		result.Reasons = append(result.Reasons, MeasurementBatchPlanReason{"DeclaredBatchSizes", "The artifact declares the batch set; batchSizes must remain omitted and subsets cannot be requested."})
	}
	condition := snapshot.Plan.Shared()
	id := MeasurementVariantID(condition)
	immutable := measurementPreflightImagesPinned(condition)
	for _, evidence := range snapshot.ExactEvidence {
		if immutable && MeasurementVariantID(evidence.Condition) == id {
			result.ConditionMatch = "exact"
			for _, batch := range evidence.Batches {
				if slices.Contains(result.PlannedBatches, batch) {
					if evidence.InProgress {
						result.InProgressBatches = append(result.InProgressBatches, batch)
					} else {
						result.MeasuredBatches = append(result.MeasuredBatches, batch)
					}
				}
			}
		}
	}
	if result.ConditionMatch == "exact" {
		if snapshot.VariantID == "" {
			result.VariantID = &id
		}
		result.Reasons = append(result.Reasons, MeasurementBatchPlanReason{"DeclarationUIDsNotRetained", "Exact matching covers retained frozen identities only; historical ModelSpec, DeviceClass and preprocessor declaration UIDs were not recorded."})
	}
	slices.Sort(result.MeasuredBatches)
	result.MeasuredBatches = slices.Compact(result.MeasuredBatches)
	slices.Sort(result.InProgressBatches)
	result.InProgressBatches = slices.Compact(result.InProgressBatches)
	result.InProgressBatches = slices.DeleteFunc(result.InProgressBatches, func(batch int) bool {
		return slices.Contains(result.MeasuredBatches, batch)
	})
	if snapshot.VariantID != "" {
		if snapshot.VariantID == id && result.ConditionMatch == "exact" {
			result.VariantID = &id
		} else {
			result.Reasons = append(result.Reasons, MeasurementBatchPlanReason{"VariantConditionChanged", "The selected variant is not confirmed for the current frozen condition; previous batch exclusions are invalid."})
		}
	}
	if !immutable {
		result.Reasons = append(result.Reasons, MeasurementBatchPlanReason{"ImmutableImagesUnconfirmed", "Image references are not all immutable digests; projected conditions are planned-only."})
	}
	for _, request := range snapshot.Requests {
		if request.Plan.SchemaVersion() != "" && MeasurementVariantID(request.Plan.Shared()) == id {
			result.Reasons = append(result.Reasons, MeasurementBatchPlanReason{"PlannedRequestOverlap", fmt.Sprintf("Request %s currently plans batches %v under matching conditions; this projected overlap alone does not confirm frozen execution or measurement.", request.Campaign.Name, request.Plan.PlannedBatchSizes())})
		}
	}
	if !snapshot.RequestsComplete {
		result.Reasons = append(result.Reasons, MeasurementBatchPlanReason{"RequestListUnconfirmed", "Current request coverage is not confirmed; an empty in-progress set does not prove no pending overlap."})
	}
	for _, batch := range result.PlannedBatches {
		if !slices.Contains(result.MeasuredBatches, batch) && !slices.Contains(result.InProgressBatches, batch) {
			result.MissingBatches = append(result.MissingBatches, batch)
		}
	}
	return result
}

func measurementPreflightImagesPinned(condition profilederivation.CurvePlanSpec) bool {
	for _, image := range []string{
		condition.RuntimeImage, condition.Artifact.Image, condition.Preprocessor.Image,
		condition.ActiveProtocol.MetricObserverImage, condition.ActiveProtocol.WorkloadObserverImage,
		condition.IdleProtocol.MetricObserverImage, condition.IdleProtocol.WorkloadObserverImage,
	} {
		_, digest, found := strings.Cut(image, "@sha256:")
		if !found || len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return false
		}
	}
	return true
}
