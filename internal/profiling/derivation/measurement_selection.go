package profilederivation

import (
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const ReplacesMeasurementAnnotation = "chill.dacs.io/replaces-batch-energy-optimization"

// MeasurementSelection identifies a measurement's logical cell, not its image,
// CPU allocation, batch sweep, physical Node or historical profile population.
// ID is namespace/name/UID, the direct catalog profile ID (before any /reuse-
// suffix). Reuse profiles retain their confirmation measurement's currentness.
// Populate MeasuredAt with MeasurementTime: (1) verified successful profile's
// MeasuredAt, (2) otherwise MeasurementComplete.LastTransitionTime, (3) otherwise
// zero. Do not substitute creation time, other conditions or an older success.
type MeasurementSelection struct {
	ID            string    `json:"id"`
	Model         string    `json:"model"`
	DeviceClass   string    `json:"deviceClass"`
	ExecutionPath string    `json:"executionPath"`
	PowerMode     string    `json:"powerMode"`
	MeasuredAt    time.Time `json:"measuredAt"`
}

// CurrentMeasurements selects the latest measurement, including failed/partial
// measurements. Callers must not prefilter successes or fall back to history.
// Explicit operator replacements take precedence; equal times use smallest ID.
func CurrentMeasurements(candidates []MeasurementSelection, replaced map[string]bool) []MeasurementSelection {
	latest := make(map[[4]string]MeasurementSelection, len(candidates))
	for _, candidate := range candidates {
		if replaced[candidate.ID] {
			continue
		}
		key := [4]string{candidate.Model, candidate.DeviceClass, candidate.ExecutionPath, candidate.PowerMode}
		old, found := latest[key]
		if !found || candidate.MeasuredAt.After(old.MeasuredAt) || candidate.MeasuredAt.Equal(old.MeasuredAt) && candidate.ID < old.ID {
			latest[key] = candidate
		}
	}
	out := make([]MeasurementSelection, 0, len(latest))
	for _, selected := range latest {
		out = append(out, selected)
	}
	slices.SortFunc(out, func(a, b MeasurementSelection) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// MeasurementTime maps the same stored timestamps for Serving and Query without
// adding fields to BEO or changing evidence. A failed/partial measurement passes
// zero profileMeasuredAt; only its MeasurementComplete condition supplies time.
func MeasurementTime(profileMeasuredAt time.Time, conditions []metav1.Condition) time.Time {
	if !profileMeasuredAt.IsZero() {
		return profileMeasuredAt
	}
	for _, condition := range conditions {
		if condition.Type == "MeasurementComplete" {
			return condition.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

// ReplacedMeasurements resolves operator annotations before recency.
// Keys of names are namespace/name; replacements maps source ID to target name
// in that same qualified form. Unknown targets do not replace another object.
func ReplacedMeasurements(replacements map[string]string, names map[string]string) map[string]bool {
	replaced := make(map[string]bool, len(replacements))
	for sourceID, name := range replacements {
		if id, found := names[name]; found && id != sourceID {
			replaced[id] = true
		}
	}
	return replaced
}
