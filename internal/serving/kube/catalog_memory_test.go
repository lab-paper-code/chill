package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestCatalogMemoryUsesOwnRetainedObservations(t *testing.T) {
	for _, tc := range []struct {
		name               string
		active, idle, want uint64
		window             bool
		observation, key   string
	}{
		{name: "idle lifetime peak", active: 4096, idle: 8192, want: 8192, observation: "idle", key: "trial-4-raw"},
		{name: "own window maximum", active: 4096, idle: 8192, want: 8192, window: true, observation: "window", key: "trial-4-raw"},
		{name: "active window maximum", active: 8192, idle: 4096, want: 8192, window: true, observation: "window", key: "trial-1-raw"},
		{name: "active largest unchanged", active: 8192, idle: 4096, want: 8192, observation: "active", key: "trial-1-raw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			change := func(index int, active *profilingrun.RuntimeResult, idle *profilingrun.IdleRuntimeResult) {
				active.MemoryBytes, idle.MemoryBytes = tc.active-1, tc.idle-1
				if index == 1 {
					active.MemoryBytes = tc.active
				}
				if index == 4 {
					idle.MemoryBytes = tc.idle
				}
				if tc.window {
					active.MemorySource, idle.MemorySource = "cgroup-v2-memory.current-samples:measurement-window", "cgroup-v2-memory.current-samples:measurement-window"
					active.MemorySamples = []profilingrun.MemorySample{{At: active.MeasurementStartedAt, Bytes: 1}, {At: active.MeasurementEndedAt, Bytes: active.MemoryBytes}}
					idle.MemorySamples = []profilingrun.MemorySample{{At: idle.MeasurementStartedAt, Bytes: 1}, {At: idle.MeasurementEndedAt, Bytes: idle.MemoryBytes}}
				}
			}
			catalog, source, _, _ := catalogEvidenceFixturePolicy(t, 4096, [6]float64{6, 6, 6, 4, 4, 4}, 3, "", change)
			view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
			if err != nil || len(view.Profiles) != 1 || len(view.Insufficiencies) != 0 {
				t.Fatalf("valid profile acceptance changed: %+v %v", view, err)
			}
			batch := view.Profiles[0].Batches[0]
			if batch.MemoryBytes != tc.want {
				t.Fatalf("memory = %d, want own retained max %d", batch.MemoryBytes, tc.want)
			}
			payload, err := json.Marshal(SummarizeProfile(view.Profiles[0]))
			if err != nil {
				t.Fatal(err)
			}
			var projected ProfileSummary
			if err := json.Unmarshal(payload, &projected); err != nil {
				t.Fatal(err)
			}
			basis := projected.Profile().Batches[0].MemoryBasis
			if basis.Observation != tc.observation || basis.Evidence.Store != "evidence" || basis.Evidence.Key != tc.key {
				t.Fatalf("chosen memory basis = %+v, want %s/%s", basis, tc.observation, tc.key)
			}
			stored, err := json.Marshal(batch)
			if err != nil || bytes.Contains(stored, []byte("memoryBasis")) {
				t.Fatalf("transient basis changed stored profile body: %s %v", stored, err)
			}
		})
	}
}
