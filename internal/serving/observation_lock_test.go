package serving

import (
	"testing"
	"time"
)

// A replacement Pod starts while the previous one still drains and holds the
// lock (D-R7-5); it waits for the release instead of exiting.
func TestObservationOwnershipWaitsForPreviousOwner(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireObservationOwnership(directory)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() {
		second, err := AcquireObservationOwnership(directory)
		if err == nil {
			err = second.Close()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		t.Fatalf("second owner acquired a held directory: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second owner still waits after the release")
	}
}
