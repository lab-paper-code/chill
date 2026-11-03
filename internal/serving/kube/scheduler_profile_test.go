package kube

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestSchedulerCaptureReusesImmutableProfileReferences(t *testing.T) {
	controller := schedulerCaptureClockController(t, nil)
	ctx := context.Background()
	diagnostics := serving.NewDiagnostics()
	directory := t.TempDir()
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: directory, Diagnostics: diagnostics,
		Store: "evidence", Retention: 2 * time.Hour, Writer: managerArchive{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(ctx); err != nil {
			t.Error(err)
		}
	})

	capture := func() serving.SchedulerSnapshot {
		t.Helper()
		snapshot, err := controller.capture(ctx, time.Now().UTC(), controller.config)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Instances) != 1 || snapshot.Instances[0].Profile == nil {
			t.Fatalf("missing captured profile: %+v", snapshot.Instances)
		}
		// Exercise both physical-instance and preparation-option references.
		return serving.SchedulerSnapshot{At: snapshot.At, Instances: snapshot.Instances,
			Options: []serving.SchedulerOption{{Instance: snapshot.Instances[0]}}}
	}
	first, second := capture(), capture()
	original := first.Instances[0].Profile
	if second.Instances[0].Profile != original {
		t.Fatal("unchanged catalog produced a new profile pointer on the next capture")
	}
	if !diagnostics.Start() {
		t.Fatal("diagnostics did not start")
	}
	record := func(stage int, snapshot serving.SchedulerSnapshot) {
		t.Helper()
		diagnostics.Mark(serving.DiagnosticTraceAnchor, serving.DiagnosticBegin, "", int64(stage))
		if err := log.SchedulerRecord(serving.SchedulerObservation{At: snapshot.At, Snapshot: &snapshot}); err != nil {
			t.Fatal(err)
		}
	}
	record(0, first)
	record(1, second)
	controller.observer.SetProfiles(controller.observer.Profiles())
	republished := capture()
	if republished.Instances[0].Profile != original {
		t.Fatal("identical catalog publication replaced the immutable profile pointer")
	}
	record(2, republished)
	profiles := controller.observer.Profiles()
	profiles[0].Condition.Firstfloor.MaxBatchSize++
	profiles[0].RuntimeMetadata.InitializedBatches[0]++
	controller.observer.SetProfiles(profiles)
	changed := capture()
	if changed.Instances[0].Profile == original || original.Condition.Firstfloor.MaxBatchSize != profiles[0].Condition.Firstfloor.MaxBatchSize-1 ||
		original.RuntimeMetadata.InitializedBatches[0] != profiles[0].RuntimeMetadata.InitializedBatches[0]-1 {
		t.Fatal("catalog change reused or mutated the old immutable profile")
	}

	record(3, changed)
	var events bytes.Buffer
	metadata, err := diagnostics.WriteEvents(&events)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.DroppedContention != 0 || metadata.Overwritten != 0 {
		t.Fatalf("incomplete diagnostics: %+v", metadata)
	}
	var marshals [4]int
	stage := 0
	for raw := events.Bytes(); len(raw) >= metadata.EventBytes; raw = raw[metadata.EventBytes:] {
		kind := serving.DiagnosticKind(binary.LittleEndian.Uint16(raw[48:50]))
		phase := serving.DiagnosticPhase(raw[51])
		if kind == serving.DiagnosticTraceAnchor {
			stage = int(binary.LittleEndian.Uint64(raw[24:32]))
		}
		if kind == serving.DiagnosticMarshal && phase == serving.DiagnosticBegin && raw[50] == 2 {
			marshals[stage]++
		}
	}
	// A new/changed profile marshals its body, catalog row and scheduler row.
	// Reused references marshal only the scheduler row, even when named twice.
	if marshals != [4]int{3, 1, 1, 3} {
		t.Fatalf("SchedulerRecord marshal counts = %v, want [3 1 1 3]", marshals)
	}

	assertPersistedSchedulerProfiles(t, directory, original, changed.Instances[0].Profile)
}

func assertPersistedSchedulerProfiles(t *testing.T, directory string, original, changed *serving.Profile) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(directory, "v1alpha5", "*", "catalog.*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var bodies [][]byte
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
			var row serving.CatalogRow
			if err := json.Unmarshal(line, &row); err != nil {
				t.Fatal(err)
			}
			if row.Key != original.ID {
				t.Fatalf("unexpected catalog reference: %+v", row)
			}
			bodies = append(bodies, row.Body)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("persisted %d profile bodies, want original and changed only", len(bodies))
	}
	for i, profile := range []*serving.Profile{original, changed} {
		want, err := json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bodies[i], want) {
			t.Fatalf("persisted profile %d differs from published body", i)
		}
	}
}
