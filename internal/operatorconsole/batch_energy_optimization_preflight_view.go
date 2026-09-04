package operatorconsole

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	}, nil
}
