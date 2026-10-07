package serving

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownCannotReopenAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	journal, err := NewJournal(JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close(context.Background()) })
	runtime, err := NewRuntime(ctx, RuntimeConfig{QueueLimit: 1, Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	definition := &Definition{Name: "workload", UID: "uid", Generation: 1, Model: Model{UID: "model", Generation: 1}, SLO: SLO{Percentile: 99, TargetLatency: time.Second}}
	if err = runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{runtime.OpenAdmission(definition.UID), runtime.Publish(definition), runtime.PublishClosed(definition, "reconcile")} {
		var domain *Error
		if !errors.As(err, &domain) || domain.Code != "ShuttingDown" {
			t.Fatalf("shutdown gate reopened: %v", err)
		}
	}
	if state, err := runtime.State(definition.Name); err != nil || state.Admission || state.Reason != "ShuttingDown" {
		t.Fatalf("shutdown state changed: %+v %v", state, err)
	}
}
