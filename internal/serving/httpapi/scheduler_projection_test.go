package httpapi

import (
	"encoding/json"
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
		struct { State schedulerReadState `json:"state"`; schedulerReadEvidence }{projected, evidence},
		struct { schedulerReadState; schedulerReadEvidence }{projected, evidence},
	} {
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) >= 8_388_608 {
			t.Fatalf("current read still scales with irrelevant raw profile samples: %d bytes", len(raw))
		}
		var wire struct { Profiles map[string]struct { Batches []struct {
			DurationNanos []float64 `json:"durationNanos"`
			DurationSampleCount int `json:"durationSampleCount"`
			EnergyBelowResolution bool `json:"energyBelowResolution"`
			MemoryBytes uint64 `json:"memoryBytes"`
		} `json:"batches"` } `json:"profiles"` }
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
