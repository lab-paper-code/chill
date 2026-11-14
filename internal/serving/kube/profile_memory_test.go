package kube

import (
	"context"
	"io"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

type countingProfileObjects struct {
	evidencestore.LocationReader
	reads int
}

func (o *countingProfileObjects) OpenLocation(ctx context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	o.reads++
	return o.LocationReader.OpenLocation(ctx, location)
}

func TestCatalogUnchangedRefreshSharesRecords(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t)
	first, err := catalog.Refresh(context.Background(), []string{source.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.Refresh(context.Background(), []string{source.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	if &first.Profiles[0].Batches[0].ServiceSamples[0] != &second.Profiles[0].Batches[0].ServiceSamples[0] {
		t.Fatal("unchanged source copied immutable records")
	}
	observer := newTestObserver(t)
	observer.SetProfiles(second.Profiles)
	_, frozen := observer.schedulerProfiles()
	if &frozen[second.Profiles[0].ID].Batches[0].ServiceSamples[0] != &first.Profiles[0].Batches[0].ServiceSamples[0] {
		t.Fatal("observer copied immutable records")
	}
}

func TestCatalogCachesUnreadableResult(t *testing.T) {
	catalog, source, objects, _ := catalogStoredFixture(t, []byte(`{"schemaVersion":"old"}`), "old")
	counter := &countingProfileObjects{LocationReader: objects}
	catalog.objects = counter
	for range 2 {
		view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
		if err != nil || len(view.Insufficiencies) != 1 {
			t.Fatalf("view=%+v err=%v", view, err)
		}
	}
	if counter.reads != 1 {
		t.Fatalf("old result read %d times", counter.reads)
	}
}

func TestObserverProfileIdentityDoesNotReadRecords(t *testing.T) {
	observer := newTestObserver(t)
	profiles := make([]serving.Profile, 20)
	for i := range profiles {
		profiles[i].ID = string(rune('a' + i))
		samples := make([]firstfloor.ServiceSample, 150000)
		for j := range samples {
			samples[j].ServiceDurationNanos = 1000000
		}
		profiles[i].Batches = []serving.BatchProfile{{Batch: 1, ServiceSamples: samples}}
	}
	observer.SetProfiles(profiles)
	_, before := observer.schedulerProfiles()
	allocs := testing.AllocsPerRun(3, func() { observer.SetProfiles(profiles) })
	if allocs > 100 {
		t.Fatalf("SetProfiles allocated per record: %.0f allocations", allocs)
	}
	// A record store is immutable. Replacing the incoming slice with nil must
	// not affect an already published identity or cause a content comparison.
	profiles[0].Batches = []serving.BatchProfile{{Batch: 1}}
	observer.SetProfiles(profiles)
	_, after := observer.schedulerProfiles()
	if before[profiles[0].ID] != after[profiles[0].ID] {
		t.Fatal("same identity compared content")
	}
	profiles[0].Reference.Key = "new-result"
	observer.SetProfiles(profiles)
	_, changed := observer.schedulerProfiles()
	if after[profiles[0].ID] == changed[profiles[0].ID] {
		t.Fatal("changed evidence identity was ignored")
	}
	profiles[0].SourceVersion = "next-resource-version"
	observer.SetProfiles(profiles)
	_, revised := observer.schedulerProfiles()
	if revised[profiles[0].ID] == changed[profiles[0].ID] {
		t.Fatal("changed source resourceVersion was ignored")
	}
}
