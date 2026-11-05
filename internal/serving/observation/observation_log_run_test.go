package observation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestUnprovedRunEvidenceRemainsUnknownAcrossBoots(t *testing.T) {
	for _, marker := range []string{"", "{", `{"clean":true}`} {
		t.Run(marker, func(t *testing.T) {
			config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
				Writer: &observationArchive{directory: t.TempDir()}}
			if marker != "" {
				if err := os.WriteFile(filepath.Join(config.Directory, "async-run.json"), []byte(marker), 0600); err != nil {
					t.Fatal(err)
				}
			}
			log, err := NewObservationLog(config)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = log.Close(context.Background()) }()
			if !log.Recovery().Unknown || !log.Health().Healthy {
				t.Fatal("unproved predecessor was treated as empty or current writer failure")
			}
			active, err := os.ReadFile(filepath.Join(config.Directory, "async-run.json"))
			if err != nil {
				t.Fatal(err)
			}
			crash := config
			crash.Directory = t.TempDir()
			if err := os.WriteFile(filepath.Join(crash.Directory, "async-run.json"), active, 0600); err != nil {
				t.Fatal(err)
			}
			restarted, err := NewObservationLog(crash)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = restarted.Close(context.Background()) }()
			if !restarted.Recovery().Unknown {
				t.Fatal("repeated boot erased uncertainty")
			}
			index, err := restarted.RawHour(context.Background(), observationHour(time.Now().UTC()))
			if err != nil || len(index.Gaps) == 0 {
				t.Fatalf("raw inventory hid unknown tail: %+v %v", index, err)
			}
			_, pending := restarted.RuntimeFacts()
			if len(pending) != 0 {
				t.Fatal("missing observation tail fabricated assignments")
			}
		})
	}
}

func TestExactTerminationClosesInfluenceWithoutErasingClear(t *testing.T) {
	ctx := context.Background()
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: &observationArchive{directory: t.TempDir()}}
	log, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close(ctx) }()
	at := time.Now().UTC()
	state := serving.InstanceState{ID: "pod/container", PodUID: "pod", Node: serving.NodeReference{Name: "node", UID: "node-uid"}, ObservedAt: at}
	if err := log.RecordRecovery(state, false); err != nil {
		t.Fatal(err)
	}
	if err := log.RecordRecovery(state, true); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	index, err := log.RawHour(ctx, observationHour(at))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, gap := range index.Gaps {
		if gap.Instance == state.ID {
			found = true
			if gap.ClearAt == nil || !gap.ClearAt.Equal(at) || !gap.Open {
				t.Fatalf("actor CLEAR invented a finite tail endpoint: %+v", gap)
			}
		}
	}
	if !found {
		t.Fatal("exact executor provenance missing")
	}
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close(ctx) }()
	ended := time.Now().UTC()
	state.Terminated, state.ObservedAt = true, ended
	if err := restarted.RecordRecovery(state, true); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	index, err = restarted.RawHour(ctx, observationHour(at))
	if err != nil {
		t.Fatal(err)
	}
	for _, gap := range index.Gaps {
		if gap.Instance == state.ID && (gap.ClearAt == nil || !gap.ClearAt.Equal(at) || gap.Open || !gap.To.Equal(ended)) {
			t.Fatalf("termination lost CLEAR or failed to close its own influence: %+v", gap)
		}
	}
	later, err := restarted.RawHour(ctx, observationHour(ended.Add(time.Hour)))
	var unavailable *serving.Error
	if err != nil && (!errors.As(err, &unavailable) || unavailable.Code != serving.ObservationUnavailableCode) {
		t.Fatal(err)
	}
	for _, gap := range later.Gaps {
		if gap.Instance == state.ID {
			t.Fatal("terminated influence poisoned a later nonoverlapping window")
		}
	}
}

func TestRunEvidenceSyncFailureCannotPublishCleanClose(t *testing.T) {
	ctx := context.Background()
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("run evidence sync unavailable")
	log.syncFile = func(file *os.File) error {
		if strings.HasPrefix(filepath.Base(file.Name()), ".async-run-") {
			return failed
		}
		return file.Sync()
	}
	if err := log.Close(ctx); !errors.Is(err, failed) {
		t.Fatalf("close ignored marker Sync error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(log.config.Directory, "async-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var marker observationRunState
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Clean || log.Health().Healthy {
		t.Fatal("failed run evidence was reported clean/healthy")
	}
	log.syncFile = (*os.File).Sync
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if log.Health().Healthy {
		t.Fatal("successful backlog recovery cleared sticky writer failure")
	}
}
