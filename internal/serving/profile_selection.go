package serving

import (
	"encoding/json"
	"fmt"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// SelectConditionProfiles keeps the latest success per frozen condition. Replaced
// targets participate in recency before removal, so older successes cannot revive.
// A newer unannotated success remains eligible, even across an explicit replacement.
func SelectConditionProfiles(profiles []Profile, replaced map[string]bool) ([]Profile, error) {
	result := make([]Profile, 0, len(profiles))
	indices := make(map[string]int, len(profiles))
	for _, profile := range profiles {
		condition := profile.Condition
		condition.Name = ""
		condition.PlannedBatchSizes = nil
		condition.RuntimeImage = ""
		payload, err := json.Marshal(struct {
			Condition    profilederivation.CurvePlanSpec `json:"condition"`
			MetricSource metricobserver.SourceIdentity   `json:"metricSource"`
		}{condition, profile.MetricSource})
		if err != nil {
			return nil, fmt.Errorf("encode frozen profile condition: %w", err)
		}
		key := string(payload)
		if index, found := indices[key]; found {
			old := result[index]
			if profile.MeasuredAt.After(old.MeasuredAt) || (profile.MeasuredAt.Equal(old.MeasuredAt) && profile.ID < old.ID) {
				result[index] = profile
			}
			continue
		}
		indices[key] = len(result)
		result = append(result, profile)
	}
	selected := result[:0]
	for _, profile := range result {
		if !replaced[profile.ID] {
			selected = append(selected, profile)
		}
	}
	return selected, nil
}
