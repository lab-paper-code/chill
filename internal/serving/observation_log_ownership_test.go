package serving

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const restoredRequest = "request"

// An assigned row without a final row survives a crash: the restart restores
// it under RestartDrain and writes one incomplete final row after completion.
func TestRestartRequiresExplicitPhysicalCompletionProof(t *testing.T) {
	ctx := context.Background()
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}}
	observationLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	d := &Definition{Name: "workload", UID: "uid", Generation: 1, Model: Model{UID: "model", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err = observationLog.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assignment := &Assignment{Instance: "physical", Node: NodeReference{Name: "node", UID: "node-uid"}, Profile: "profile", DispatchedAt: now}
	for _, event := range []RequestEvent{
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: phaseReceived, At: now, ReceivedAt: now},
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: phaseAssigned, At: now, ReceivedAt: now, Assignment: assignment},
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "backend_finished", At: now, NativeProgress: &BackendProgress{Phase: "finished", ExecutionComplete: false}},
		{RequestID: restoredRequest, WorkloadUID: d.UID, Definition: d.Identity(), Kind: eventResponseFinished, At: now, Outcome: "canceled"},
	} {
		if err = observationLog.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err = observationLog.Close(ctx); err != nil { // Crash: the open row is lost.
		t.Fatal(err)
	}
	restartedLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewRuntime(ctx, RuntimeConfig{QueueLimit: 1, ObservationLog: restartedLog})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.cancel()
	if restored.workloads[d.UID].reason != "RestartDrain" {
		t.Fatalf("restored assignment did not close admission: %+v", restored.workloads[d.UID])
	}
	if detached, err := restored.DetachInstance("physical"); err != nil || detached {
		t.Fatalf("bare finished phase freed restarted physical ownership: %v %v", detached, err)
	}
	if err = restored.ConfirmInstanceStopped("physical"); err != nil {
		t.Fatal(err)
	}
	if err = restartedLog.Close(ctx); err != nil {
		t.Fatal(err)
	}
	finals := 0
	for _, row := range readRequestRows(t, config.Directory) {
		if row.Kind == requestRowFinal {
			finals++
			if row.RequestID != restoredRequest || row.Outcome != outcomeIncomplete || row.Error != "EntrypointRestarted" {
				t.Fatalf("restored request closed with a wrong final row: %+v", row)
			}
		}
	}
	if finals != 1 {
		t.Fatalf("restored request has %d final rows", finals)
	}
	confirmedLog, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = confirmedLog.Close(ctx) }()
	confirmed, err := NewRuntime(ctx, RuntimeConfig{QueueLimit: 1, ObservationLog: confirmedLog})
	if err != nil {
		t.Fatal(err)
	}
	defer confirmed.cancel()
	if !confirmed.Drained(d.UID) {
		t.Fatal("actual container termination proof lost at restart")
	}
}

func readRequestRows(t *testing.T, directory string) []RequestRow {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(directory, observationVersion, "*", observationRequestStream+".*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []RequestRow
	for _, name := range names {
		file, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(file)
		for decoder.More() {
			var row RequestRow
			if err := decoder.Decode(&row); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return rows
}
