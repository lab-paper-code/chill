package httpapi

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func TestSchedulerCurrentProjectionBoundsRawProfileSamples(t *testing.T) {
	samples := make([]float64, 2_000_000)
	for i := range samples {
		samples[i] = 123456789
	}
	profile := &serving.Profile{ID: "measured", Batches: []serving.BatchProfile{{
		Batch: 1, DurationNanos: samples, EnergyPerInputJoules: 0, EnergyBelowResolution: true, MemoryBytes: 8192,
	}}}
	instance := serving.SchedulerInstance{ID: "physical", Profile: profile}
	state := servingkube.SchedulerControllerState{Snapshot: serving.SchedulerSnapshot{
		Instances: []serving.SchedulerInstance{instance}, Options: []serving.SchedulerOption{{Instance: instance}},
	}, Decision: serving.SchedulerDecision{Keep: serving.SchedulerCandidate{ID: "keep", Instances: []serving.SchedulerInstance{instance}}}}
	evidence := newSchedulerReadEvidence()
	projected := evidence.state(&state)
	for _, response := range []any{
		struct {
			State schedulerReadState `json:"state"`
			schedulerReadEvidence
		}{projected, evidence},
		struct {
			schedulerReadState
			schedulerReadEvidence
		}{projected, evidence},
	} {
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) >= 2_000_000 {
			t.Fatalf("current read still scales with irrelevant raw profile samples: %d bytes", len(raw))
		}
		var wire struct {
			Profiles map[string]struct {
				Batches []struct {
					DurationNanos         []float64 `json:"durationNanos"`
					DurationSampleCount   int       `json:"durationSampleCount"`
					EnergyBelowResolution bool      `json:"energyBelowResolution"`
					MemoryBytes           uint64    `json:"memoryBytes"`
				} `json:"batches"`
			} `json:"profiles"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		batch := wire.Profiles["measured"].Batches[0]
		if len(batch.DurationNanos) != 0 || batch.DurationSampleCount != len(samples) || !batch.EnergyBelowResolution || batch.MemoryBytes != 8192 {
			t.Fatalf("profile projection lost scalar evidence: %+v", batch)
		}
	}
	if state.Snapshot.Instances[0].Profile != profile || len(profile.Batches[0].DurationNanos) != len(samples) {
		t.Fatal("current read mutated scheduler evidence")
	}
}

func TestSchedulerCurrentProjectionBoundsUnusedPopulations(t *testing.T) {
	definition := &serving.Definition{Name: "service", UID: "workload", Model: serving.Model{Name: strings.Repeat("model", 512)}}
	profile := &serving.Profile{ID: "in-use"}
	instance := serving.SchedulerInstance{ID: "physical", WorkloadUID: definition.UID, Ready: true, Profile: profile}
	state := servingkube.SchedulerControllerState{Snapshot: serving.SchedulerSnapshot{
		Instances: []serving.SchedulerInstance{instance}, Workloads: []*serving.Definition{definition},
		Outstanding: []serving.SchedulerRequest{{ID: "pending", Definition: definition, Phase: "pending"}},
		Arrivals:    make([]serving.SchedulerArrival, 2_000),
		Options:     make([]serving.SchedulerOption, 500),
	}, Decision: serving.SchedulerDecision{Keep: serving.SchedulerCandidate{ID: "keep", Instances: []serving.SchedulerInstance{instance}}}}
	for i := range state.Snapshot.Arrivals {
		state.Snapshot.Arrivals[i] = serving.SchedulerArrival{ID: strconv.Itoa(i), Definition: definition, Accepted: true}
	}
	for i := range state.Snapshot.Options {
		optionProfile := &serving.Profile{ID: "option-" + strconv.Itoa(i)}
		optionProfile.Reference.Key = strings.Repeat("evidence", 1_024)
		state.Snapshot.Options[i].Instance.Profile = optionProfile
	}
	full, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= 8_388_608 {
		t.Fatalf("fixture no longer exercises a relay-sized raw population: %d bytes", len(full))
	}
	evidence := newSchedulerReadEvidence()
	projected := evidence.state(&state)
	for _, response := range []any{
		struct {
			State schedulerReadState `json:"state"`
			schedulerReadEvidence
		}{projected, evidence},
		struct {
			schedulerReadState
			schedulerReadEvidence
		}{projected, evidence},
	} {
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("full=%d compact=%d bytes", len(full), len(raw))
		if len(raw) >= 2_000_000 {
			t.Fatalf("current read still scales with unused option/arrival populations: %d bytes", len(raw))
		}
	}
	if projected.Snapshot.ArrivalCount == nil || *projected.Snapshot.ArrivalCount != 2_000 ||
		projected.Snapshot.OptionCount == nil || *projected.Snapshot.OptionCount != 500 ||
		len(projected.Snapshot.Arrivals) != 0 || len(projected.Snapshot.Options) != 0 {
		t.Fatal("current read did not replace unused populations with exact counts")
	}
	if len(evidence.Profiles) != 1 || evidence.Profiles[profile.ID] == nil ||
		len(projected.Snapshot.Outstanding) != 1 || projected.Snapshot.Outstanding[0].Definition != definition ||
		projected.Decision.Keep.Instances[0].ID != instance.ID || !projected.Snapshot.Instances[0].Ready {
		t.Fatal("current read lost dashboard instance, pending request or decision evidence")
	}
	if len(state.Snapshot.Arrivals) != 2_000 || state.Snapshot.Arrivals[0].Definition != definition ||
		len(state.Snapshot.Options) != 500 || state.Snapshot.Options[0].Instance.Profile == nil {
		t.Fatal("current read mutated the raw state")
	}
	history := newSchedulerReadEvidence()
	records := history.observations([]serving.SchedulerObservation{{Snapshot: &state.Snapshot}})
	if len(records[0].Snapshot.Options) != 500 || len(records[0].Snapshot.Arrivals) != 2_000 || len(history.Profiles) != 501 {
		t.Fatal("current read changed retained history evidence")
	}
	if records[0].Snapshot.ArrivalCount != nil || records[0].Snapshot.OptionCount != nil {
		t.Fatal("current read counts leaked into historical reads")
	}
}
