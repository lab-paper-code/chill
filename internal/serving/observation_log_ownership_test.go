package serving

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestartIgnoresUnpublishedWindowStaging(t *testing.T) {
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}}
	if err := os.Mkdir(filepath.Join(config.Directory, ".starting-interrupted"), 0700); err != nil {
		t.Fatal(err)
	}
	observationLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatalf("interrupted window creation blocked owner startup: %v", err)
	}
	if err = observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFailedRotationPreservesCompletedWindow(t *testing.T) {
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}}
	observationLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := observationLog.finish(); err != nil {
		t.Fatal(err)
	}
	closedAt := *observationLog.Health().Windows[0].ClosedAt
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	observationLog.config.Directory = blocked

	if err := observationLog.RequestEvent(RequestEvent{RequestID: "r", WorkloadUID: "uid", Kind: "received", At: time.Now().UTC()}); err == nil {
		t.Fatal("failed window publication accepted an event")
	}
	if err := observationLog.Flush(context.Background()); err == nil {
		t.Fatal("failed window publication was not reported by flush")
	}
	for _, window := range observationLog.Health().Windows {
		if window.Incomplete {
			t.Fatalf("finished window %s was flagged incomplete by a failed start", window.ID)
		}
	}
	observationLog.config.Directory = config.Directory
	if err := observationLog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if window := observationLog.Health().Windows[0]; window.ClosedAt == nil || !window.ClosedAt.Equal(closedAt) {
		t.Fatalf("failed rotation changed completed window boundary: before=%v after=%v", closedAt, window.ClosedAt)
	}
}

func TestRestartRequiresExplicitPhysicalCompletionProof(t *testing.T) {
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	d := &Definition{Name: "workload", UID: "uid", Generation: 1, Model: Model{UID: "model", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err = observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assignment := &Assignment{Instance: "physical", Node: NodeReference{Name: "node", UID: "node-uid"}, Pool: "pool", Profile: "profile"}
	for _, event := range []RequestEvent{
		{RequestID: "request", WorkloadUID: d.UID, Definition: d.Identity(), Kind: "assigned", At: now, ReceivedAt: now, Assignment: assignment},
		{RequestID: "request", WorkloadUID: d.UID, Definition: d.Identity(), Kind: "backend_finished", At: now, NativeProgress: &BackendProgress{Phase: "finished", ExecutionComplete: false}},
		{RequestID: "request", WorkloadUID: d.UID, Definition: d.Identity(), Kind: "response_finished", At: now, Outcome: "canceled"},
	} {
		if err = observationLog.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 1, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.cancel()
	if detached, err := restored.DetachInstance("physical"); err != nil || detached {
		t.Fatalf("bare finished phase freed restarted physical ownership: %v %v", detached, err)
	}
	if err = restored.ConfirmInstanceStopped("physical"); err != nil {
		t.Fatal(err)
	}
	confirmed, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 1, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer confirmed.cancel()
	if !confirmed.Drained(d.UID) {
		t.Fatal("actual container termination proof lost at restart")
	}
}
