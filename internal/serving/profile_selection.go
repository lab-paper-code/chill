package serving

import (
	"encoding/json"
	"fmt"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// SelectConditionProfiles keeps the latest success per frozen condition. Replaced
// targets participate in recency before removal, so older successes cannot revive.
// A newer unannotated success remains eligible, even across an explicit replacement.
func SelectConditionProfiles(profiles []Profile, replaced map[string]bool) ([]Profile, error) {
	result := make([]Profile, 0, len(profiles))
	indices := make(map[string]int, len(profiles))
	for _, profile := range profiles {
		key, err := profileConditionKey(profile)
		if err != nil {
			return nil, err
		}
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

// ExcludeReplacedConditionProfiles retires only explicit targets and their older
// lineage. It preserves every other row, including reuse confirmations which must
// be separated from direct sources before latest-per-condition selection.
func ExcludeReplacedConditionProfiles(profiles []Profile, replaced map[string]bool) ([]Profile, error) {
	if len(replaced) == 0 {
		return profiles, nil
	}
	cutoffs := make(map[string]Profile, len(replaced))
	for _, profile := range profiles {
		if !replaced[profile.ID] {
			continue
		}
		key, err := profileConditionKey(profile)
		if err != nil {
			return nil, err
		}
		old, found := cutoffs[key]
		if !found || profile.MeasuredAt.After(old.MeasuredAt) ||
			(profile.MeasuredAt.Equal(old.MeasuredAt) && profile.ID < old.ID) {
			cutoffs[key] = profile
		}
	}
	result := make([]Profile, 0, len(profiles))
	for _, profile := range profiles {
		if replaced[profile.ID] {
			continue
		}
		key, err := profileConditionKey(profile)
		if err != nil {
			return nil, err
		}
		target, found := cutoffs[key]
		if found && (profile.MeasuredAt.Before(target.MeasuredAt) ||
			(profile.MeasuredAt.Equal(target.MeasuredAt) && profile.ID > target.ID)) {
			continue
		}
		result = append(result, profile)
	}
	return result, nil
}

func profileConditionKey(profile Profile) (string, error) {
	payload, err := json.Marshal(profilederivation.PerformanceCondition(profile.Condition))
	if err != nil {
		return "", fmt.Errorf("encode frozen profile condition: %w", err)
	}
	return string(payload), nil
}
