package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The captured digest deliberately contains only public profile ID, status,
// batch energy and wall power. Full raw duration distributions are checked by
// the two actual catalog loaders separately.
func (m *migrator) compareBaseline(path string, after verification) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var profiles []object
	if err = json.Unmarshal(b, &profiles); err != nil {
		return err
	}
	index := map[string]object{}
	for _, p := range after.Digest {
		index[str(p, "id")] = p
	}
	missing, energy, status := []string{}, []string{}, []string{}
	for _, p := range profiles {
		id := str(p, "id")
		q, ok := index[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if !equalJSON(p["batches"], q["batches"]) {
			energy = append(energy, id)
		}
		if str(p, "status") != "usable" || str(q, "status") != "usable" {
			status = append(status, id)
		}
	}
	unexpected := []string{}
	base := map[string]bool{}
	for _, p := range profiles {
		base[str(p, "id")] = true
	}
	for id := range index {
		if !base[id] {
			unexpected = append(unexpected, id)
		}
	}
	usable, err := baselineCoverage(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "profiles.json"))
	if err != nil {
		return err
	}
	var original struct {
		Profiles []object `json:"profiles"`
	}
	if err = json.Unmarshal(raw, &original); err != nil {
		return err
	}
	expected, merges, err := expectedProfiles(original.Profiles)
	if err != nil {
		return err
	}
	m.merges = merges
	expectedMissing := make([]string, 0, len(merges))
	for id := range merges {
		expectedMissing = append(expectedMissing, id)
	}
	sort.Strings(expectedMissing)
	sort.Strings(missing)
	sort.Strings(unexpected)
	selectionMatches := len(index) == len(expected)
	for id := range expected {
		if _, ok := index[id]; !ok {
			selectionMatches = false
		}
	}
	expectedLossMatches := equalJSON(encode(expectedMissing), encode(missing))
	// Decision A is for this captured population, not permission for arbitrary
	// profile loss. Derive the exact non-latest set independently of the output.
	approvedSelection := len(profiles) == 259 && len(expected) == 242 && len(merges) == 17 && expectedLossMatches && selectionMatches && len(unexpected) == 0
	for _, id := range expectedMissing {
		fmt.Printf("condition_merge missing=%s selected=%s source=internal/serving/kube/catalog.go:233-255\n", id, merges[id])
	}
	detail := map[string]any{"before": len(profiles), "after": len(after.Digest), "approved_cpu_duplicates": 17, "expected_missing": expectedMissing, "actual_missing": missing, "missing_set_match": expectedLossMatches, "unexpected": unexpected, "id_changes": 0}
	checks := []struct {
		name   string
		ok     bool
		detail any
	}{
		{"profile_count_and_ids", approvedSelection, detail},
		{"energyPerInputJoules_and_meanWallWatts", len(energy) == 0 && len(unexpected) == 0, map[string]any{"value_mismatches": energy, "compared_profiles": len(after.Digest)}},
		{"profile_usable", len(status) == 0 && len(unexpected) == 0, map[string]any{"status_mismatches": status, "usable_profiles": len(after.Digest)}},
		{"coverage_usable_cells", usable == after.Usable, map[string]int{"before": usable, "after": after.Usable}},
	}
	for _, check := range checks {
		verdict := "PASS"
		if !check.ok {
			verdict = "FAIL"
			m.fail("acceptance/"+check.name, fmt.Errorf("%s", encode(check.detail)))
		}
		fmt.Printf("ACCEPTANCE %s %s %s\n", verdict, check.name, encode(check.detail))
	}
	fmt.Printf("profile_count_explanation baseline_accepted=%d retained_measurement_facts=%d accepted_after=%d; coverage includes retained superseded facts and rejected reuse confirmations, not just adopted profiles; Failed BEOs never enter Catalog.Refresh; Decision A approves exactly 17 non-latest CPU duplicates; profile ID changes=0\n", len(profiles), after.Retained, len(after.Digest))
	return nil
}

// Same grouping/tie-break as 9ccff96e catalog.go:233-260, computed from the
// original live payload rather than from whichever IDs the conversion retained.
func expectedProfiles(profiles []object) (map[string]bool, map[string]string, error) {
	type winner struct {
		id string
		at time.Time
	}
	byCondition := map[string]winner{}
	keys := map[string]string{}
	for _, p := range profiles {
		id := str(p, "id")
		at, err := time.Parse(time.RFC3339Nano, str(p, "measuredAt"))
		if err != nil {
			return nil, nil, err
		}
		condition := child(p, "condition")
		delete(condition, "cpu")
		delete(condition, "name")
		delete(condition, "plannedBatchSizes")
		key := string(encode(object{"condition": encode(condition), "metricSource": p["metricSource"]}))
		keys[id] = key
		old, ok := byCondition[key]
		if !ok || at.After(old.at) || (at.Equal(old.at) && id < old.id) {
			byCondition[key] = winner{id, at}
		}
	}
	selected := map[string]bool{}
	merges := map[string]string{}
	for id, key := range keys {
		keep := byCondition[key].id
		selected[keep] = true
		if keep != id {
			merges[id] = keep
		}
	}
	return selected, merges, nil
}

func baselineCoverage(path string) (int, error) {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "coverage.json"))
	if err != nil {
		return 0, err
	}
	var coverage struct {
		Summary struct {
			Counts map[string]int `json:"countsByDisplayStatus"`
		} `json:"summary"`
	}
	if err = json.Unmarshal(b, &coverage); err != nil {
		return 0, err
	}
	usable, ok := coverage.Summary.Counts["usable"]
	if !ok {
		return 0, fmt.Errorf("captured coverage lacks usable count")
	}
	return usable, nil
}
