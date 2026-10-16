package serving

import "testing"

func TestObservationDirectoryHasOneProcessOwner(t *testing.T) {
	directory := t.TempDir()
	first, err := AcquireObservationOwnership(directory)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := AcquireObservationOwnership(directory); err == nil {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
		if err := first.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("concurrent observation log owner acquired directory")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := AcquireObservationOwnership(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
}
