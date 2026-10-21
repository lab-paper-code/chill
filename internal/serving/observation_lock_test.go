package serving

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestObservationDirectoryHasOneProcessOwner(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireObservationOwnership(context.Background(), directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	waited := 0
	if second, err := AcquireObservationOwnership(ctx, directory, func() { waited++ }); err == nil {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
		if err := first.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("concurrent observation log owner acquired directory")
	} else if !errors.Is(err, context.DeadlineExceeded) || waited != 1 {
		t.Fatalf("contended acquisition ended with %v after %d wait reports, want the context error after one", err, waited)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := AcquireObservationOwnership(context.Background(), directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
}

// A replacement Pod starts while the previous one still drains and holds the
// lock (D-R7-5); it waits for the release instead of exiting.
func TestObservationOwnershipWaitsForPreviousOwner(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireObservationOwnership(context.Background(), directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	time.AfterFunc(100*time.Millisecond, func() { released <- first.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waited := 0
	second, err := AcquireObservationOwnership(ctx, directory, func() { waited++ })
	if err != nil {
		t.Fatalf("acquisition after the previous owner released: %v", err)
	}
	if err = <-released; err != nil {
		t.Fatal(err)
	}
	if waited != 1 {
		t.Fatalf("wait reported %d times, want once", waited)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
}
