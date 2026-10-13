package serving

import (
	"context"
	"testing"
	"time"
)

func TestRestartRequiresExplicitPhysicalCompletionProof(t *testing.T) {
	j, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := j.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	d := &Definition{Name: "workload", UID: "uid", Generation: 1, Model: Model{UID: "model", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err = j.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assignment := &Assignment{Instance: "physical", Node: NodeReference{Name: "node", UID: "node-uid"}, Pool: "pool", Profile: "profile"}
	for _, event := range []RequestEvent{
		{RequestID: "request", WorkloadUID: d.UID, Definition: d.Identity(), Kind: "assigned", At: now, ReceivedAt: now, Assignment: assignment},
		{RequestID: "request", WorkloadUID: d.UID, Definition: d.Identity(), Kind: "backend_finished", At: now, NativeProgress: &BackendProgress{Phase: "finished", ExecutionComplete: false}},
		{RequestID: "request", WorkloadUID: d.UID, Definition: d.Identity(), Kind: "response_finished", At: now, Outcome: "canceled"},
	} {
		if err = j.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 1, Journal: j})
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
	confirmed, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 1, Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	defer confirmed.cancel()
	if !confirmed.Drained(d.UID) {
		t.Fatal("actual container termination proof lost at restart")
	}
}
