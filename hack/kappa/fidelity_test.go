package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// TestDecisionFidelity replays an unmodified captured snapshot with its frozen
// settings. The optional output preserves decisions and per-stage timings for
// before/after comparisons without making wall-clock values a correctness gate.
func TestDecisionFidelity(t *testing.T) {
	path := os.Getenv("CHILL_FIDELITY_INPUT")
	if path == "" {
		t.Skip("set CHILL_FIDELITY_INPUT to a hydrated decision event")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input record
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeSnapshot(input.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if input.Decision == nil {
		t.Fatal("decision with frozen settings required")
	}
	profilesData, err := os.ReadFile(os.Getenv("CHILL_FIDELITY_PROFILES"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Profiles []serving.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(profilesData, &catalog); err != nil {
		t.Fatal(err)
	}
	profiles := make(map[string]*serving.Profile)
	for j := range catalog.Profiles {
		profiles[catalog.Profiles[j].ID] = &catalog.Profiles[j]
	}
	if err := hydrateReplayInstances(snapshot.Instances, profiles); err != nil {
		t.Fatal(err)
	}
	for j := range snapshot.Options {
		if err := hydrateReplayInstances([]serving.SchedulerInstance{snapshot.Options[j].Instance}, profiles); err != nil {
			t.Fatal(err)
		}
		snapshot.Options[j].Instance.Profile = profiles[snapshot.Options[j].Instance.ProfileID]
	}
	for _, reservation := range snapshot.Reservations {
		if reservation.Target != nil {
			if err := hydrateReplayInstances(reservation.Target.Instances, profiles); err != nil {
				t.Fatal(err)
			}
		}
	}
	hydrated, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = decodeSnapshot(hydrated)
	if err != nil {
		t.Fatal(err)
	}
	settings := input.Decision.Settings
	var results []serving.SchedulerDecision
	for range 3 {
		started := time.Now()
		decision, err := serving.EvaluateScheduler(context.Background(), settings, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("evaluate=%s timing=%+v", time.Since(started), decision.EvaluationTiming)
		results = append(results, decision)
	}
	if path := os.Getenv("CHILL_FIDELITY_OUTPUT"); path != "" {
		data, err := json.Marshal(results)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
