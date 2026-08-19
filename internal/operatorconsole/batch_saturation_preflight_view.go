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

// BatchSaturationPreflightSnapshot pairs one campaign a caller intends to
// apply with the outcome of resolving the plan it selects. An intent that does
// not resolve is the answer rather than a failed read, so the reason travels
// beside the plan instead of replacing it.
type BatchSaturationPreflightSnapshot struct {
	ReadAt   time.Time
	Campaign edgev1alpha1.BatchSaturation
	Plan     profilederivation.CurvePlan
	Reason   string
}

const BatchSaturationPreflightAPIVersion = "console.chill.dacs.io/v1alpha1"

// BatchSaturationPreflight is the bounded read model for one campaign that has
// not been applied. The plan is republished as the canonical JSON the campaign
// would have recorded, so what a preflight shows and what a run would freeze
// cannot drift apart.
type BatchSaturationPreflight struct {
	APIVersion string                           `json:"apiVersion"`
	Kind       string                           `json:"kind"`
	ReadAt     time.Time                        `json:"readAt"`
	Campaign   BatchSaturationPreflightCampaign `json:"campaign"`
	Resolves   bool                             `json:"resolves"`
	Reason     string                           `json:"reason,omitempty"`
	Plan       json.RawMessage                  `json:"plan,omitempty"`
}

type BatchSaturationPreflightCampaign struct {
	Namespace string                           `json:"namespace"`
	Name      string                           `json:"name"`
	Spec      edgev1alpha1.BatchSaturationSpec `json:"spec"`
}

func BuildBatchSaturationPreflight(
	snapshot BatchSaturationPreflightSnapshot,
) (BatchSaturationPreflight, error) {
	resolved := snapshot.Plan.SchemaVersion() != ""
	switch {
	case resolved && strings.TrimSpace(snapshot.Reason) != "":
		return BatchSaturationPreflight{}, errors.New("a resolved preflight carries no reason")
	case !resolved && strings.TrimSpace(snapshot.Reason) == "":
		return BatchSaturationPreflight{}, errors.New("an unresolved preflight requires a reason")
	}

	var plan json.RawMessage
	if resolved {
		canonical, err := snapshot.Plan.CanonicalJSON()
		if err != nil {
			return BatchSaturationPreflight{}, fmt.Errorf("encode batch-saturation preflight plan: %w", err)
		}
		plan = canonical
	}

	campaign := snapshot.Campaign.DeepCopy()
	return BatchSaturationPreflight{
		APIVersion: BatchSaturationPreflightAPIVersion,
		Kind:       "BatchSaturationPreflight",
		ReadAt:     snapshot.ReadAt.UTC(),
		Campaign: BatchSaturationPreflightCampaign{
			Namespace: campaign.Namespace,
			Name:      campaign.Name,
			Spec:      campaign.Spec,
		},
		Resolves: resolved,
		Reason:   snapshot.Reason,
		Plan:     plan,
	}, nil
}
