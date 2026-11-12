package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func TestSchedulerCapturedProfileVersionsResolveFromPersistedCatalog(t *testing.T) {
	controller := schedulerCaptureClockController(t, nil)
	ctx := context.Background()
	directory := t.TempDir()
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: directory,
		Store: "evidence", Retention: 2 * time.Hour, Writer: managerArchive{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	type profileVersion struct {
		key         string
		maxBatch    int32
		initialized []int
	}
	expected := map[time.Time]profileVersion{}
	recordCapture := func() {
		t.Helper()
		snapshot, err := controller.capture(ctx, time.Now().UTC(), controller.config)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Instances) != 1 || snapshot.Instances[0].Profile == nil {
			t.Fatalf("missing captured profile: %+v", snapshot.Instances)
		}
		snapshot.At = snapshot.At.UTC()
		profile := snapshot.Instances[0].Profile
		expected[snapshot.At] = profileVersion{key: profile.ID, maxBatch: profile.Condition.Firstfloor.MaxBatchSize,
			initialized: slices.Clone(profile.RuntimeMetadata.InitializedBatches)}
		// Both consumer references must resolve to the version visible at capture.
		row := serving.SchedulerSnapshot{At: snapshot.At, Instances: snapshot.Instances,
			Options: []serving.SchedulerOption{{Instance: snapshot.Instances[0]}}}
		if err := log.SchedulerRecord(observation.SchedulerObservation{At: snapshot.At, Snapshot: &row}); err != nil {
			t.Fatal(err)
		}
	}
	recordCapture()
	recordCapture()
	controller.observer.SetProfiles(controller.observer.Profiles())
	recordCapture()
	profiles := controller.observer.Profiles()
	profiles[0].Condition.Firstfloor.MaxBatchSize++
	profiles[0].RuntimeMetadata.InitializedBatches[0]++
	controller.observer.SetProfiles(profiles)
	recordCapture()
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}

	var catalog []observation.CatalogRow
	readRows := func(stream string, consume func([]byte)) {
		t.Helper()
		paths, err := filepath.Glob(filepath.Join(directory, "v1alpha7", "*", stream+".*.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
				consume(line)
			}
		}
	}
	readRows("catalog", func(raw []byte) {
		var row observation.CatalogRow
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, row)
	})
	readRows("scheduler", func(raw []byte) {
		var row observation.SchedulerObservation
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		want, ok := expected[row.At]
		if !ok || row.Snapshot == nil {
			t.Fatalf("scheduler record lost its captured version: %+v", row)
		}
		for _, instance := range []serving.SchedulerInstance{row.Snapshot.Instances[0], row.Snapshot.Options[0].Instance} {
			if instance.ProfileID != want.key {
				t.Fatalf("scheduler reference does not name captured profile: %+v", instance)
			}
			var selected *observation.CatalogRow
			for index := range catalog {
				candidate := &catalog[index]
				if candidate.Kind == "profile" && candidate.Key == instance.ProfileID && !candidate.At.After(row.At) &&
					(selected == nil || !candidate.At.Before(selected.At)) {
					selected = candidate
				}
			}
			if selected == nil {
				t.Fatal("scheduler reference has no preceding catalog body")
			}
			var profile serving.Profile
			if err := json.Unmarshal(selected.Body, &profile); err != nil {
				t.Fatal(err)
			}
			if profile.Condition.Firstfloor.MaxBatchSize != want.maxBatch ||
				!slices.Equal(profile.RuntimeMetadata.InitializedBatches, want.initialized) {
				t.Fatalf("catalog reference resolved the wrong published version: %+v", profile)
			}
		}
		delete(expected, row.At)
	})
	if len(expected) != 0 {
		t.Fatalf("captured scheduler versions were not persisted: %v", expected)
	}
}
