package observation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestSuccessfulCreateExcludesOnlyPriorCrashInfluence(t *testing.T) {
	ctx := context.Background()
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: time.Hour, Writer: &observationArchive{directory: t.TempDir()}}
	log, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close(ctx) }()
	at := time.Now().UTC()
	created := serving.InstanceState{ID: "new-pod/container", PodUID: "new-pod", Node: serving.NodeReference{Name: "node", UID: "node-uid"}, ObservedAt: at}
	old := serving.InstanceState{ID: "old-pod/container", PodUID: "old-pod", Node: created.Node, ObservedAt: at}
	// A normal watch may discover the Pod immediately after API Create returns.
	for _, state := range []serving.InstanceState{created, old} {
		if err := log.RecordRecovery(state, false); err != nil {
			t.Fatal(err)
		}
	}
	proof := serving.InstanceState{PodUID: created.PodUID, Node: created.Node, CreatedThisRun: true}
	if err := log.RecordRecovery(proof, false); err != nil {
		t.Fatal(err)
	}
	if err := log.RecordRecovery(created, true); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	assertCreationInfluence(t, log, created.PodUID, false)
	assertCreationInfluence(t, log, old.PodUID, true)
	if err := log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	clean, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clean.Close(ctx) }()
	if err := clean.RecordRecovery(created, false); err != nil {
		t.Fatal(err)
	}
	if err := clean.RecordRecovery(created, true); err != nil {
		t.Fatal(err)
	}
	if err := clean.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	assertCreationInfluence(t, clean, created.PodUID, false)
	assertCreationInfluence(t, clean, old.PodUID, true)
	// Copy active evidence without Close: a new lost-tail root applies even to
	// the Pod whose exact creation excluded older roots.
	active, err := os.ReadFile(filepath.Join(config.Directory, "async-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	crashConfig := config
	crashConfig.Directory = t.TempDir()
	if err := os.WriteFile(filepath.Join(crashConfig.Directory, "async-run.json"), active, 0600); err != nil {
		t.Fatal(err)
	}
	crash, err := NewObservationLog(crashConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = crash.Close(ctx) }()
	if err := crash.RecordRecovery(created, false); err != nil {
		t.Fatal(err)
	}
	if err := crash.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	assertCreationInfluence(t, crash, created.PodUID, true)
}

func assertCreationInfluence(t *testing.T, log *ObservationLog, pod string, want bool) {
	t.Helper()
	index, err := log.RawHour(context.Background(), observationHour(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, gap := range index.Gaps {
		if gap.PodUID == pod && gap.Open {
			found = true
		}
	}
	if found != want {
		t.Fatalf("raw influence for %s = %v, want %v: %+v", pod, found, want, index.Gaps)
	}
}
