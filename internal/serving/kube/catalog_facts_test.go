package kube

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type countedCatalogObjects struct {
	objects catalogEvidenceObjects
	reads int
}

func (c *countedCatalogObjects) OpenLocation(ctx context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	c.reads++
	return c.objects.OpenLocation(ctx, location)
}

type failingCatalogList struct {
	client.Client
}

func (c failingCatalogList) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("catalog source unavailable")
}

func TestCatalogMeasurementFactsReuseLoadedRaw(t *testing.T) {
	fixture, source, objects, _ := catalogEvidenceFixture(t, 4096)
	counted := &countedCatalogObjects{objects: objects}
	catalog, err := NewCatalog(fixture.store, counted, fixture.maxObjectBytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(first.Profiles) != 1 {
		t.Fatalf("catalog=%+v error=%v", first, err)
	}
	if counted.reads != 14 {
		t.Fatalf("facts added raw I/O: got %d reads, want result+plan+6 execution+6 raw", counted.reads)
	}
	id := first.Profiles[0].ID
	facts := first.MeasurementFacts[id]
	if len(facts.Batches) != 1 {
		t.Fatal(facts)
	}
	row := facts.Batches[0]
	if row.P95Milliseconds == nil || *row.P95Milliseconds != 2 || row.LatencySampleCount != 6 ||
		row.ActiveTrialCount != 3 || row.IdleTrialCount != 3 || row.CompletedItems != 6 || row.CompletedCalls != 6 ||
		row.IdleWatts == nil || *row.IdleWatts != 4 || row.ActiveWatts == nil || *row.ActiveWatts != 6 ||
		row.TotalJoulesPerItem == nil || *row.TotalJoulesPerItem != 90 || row.IncrementalJoulesPerItem == nil || *row.IncrementalJoulesPerItem != 30 ||
		row.Confidence == nil || row.Confidence.HalfWidth != 0 || row.Confidence.Level != .95 || len(row.Quality) != 6 || len(row.Sources) != 13 {
		t.Fatalf("raw-derived facts changed: %+v", row)
	}
	if row.PowerSource.Kind == "" || !row.Quality[0].SourceTimestampAbsent || row.Quality[0].PolicyPassed != nil {
		t.Fatalf("source/capture facts invented a quality policy: %+v", row)
	}
	*row.P95Milliseconds = 999
	facts.Condition.CPU.AffinityCPUs[0] = 999
	second, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || counted.reads != 14 || !second.SourceCheckedAt.After(first.SourceCheckedAt) || second.Revision != first.Revision {
		t.Fatalf("unchanged refresh did not check source without raw I/O: %+v %v reads=%d", second, err, counted.reads)
	}
	if *second.MeasurementFacts[id].Batches[0].P95Milliseconds != 2 || second.MeasurementFacts[id].Condition.CPU.AffinityCPUs[0] == 999 {
		t.Fatal("caller mutated cached measurement facts")
	}
	original := catalog.store.client
	catalog.store.client = failingCatalogList{Client: original}
	if _, err := catalog.Refresh(ctx, []string{source.Namespace}); err == nil {
		t.Fatal("source failure was hidden")
	}
	checked, sourceError, revision := catalog.SourceStatus()
	if checked != second.SourceCheckedAt || sourceError == "" || revision != second.Revision {
		t.Fatalf("failure changed last successful source check: %v %q %q", checked, sourceError, revision)
	}
	catalog.store.client = original
}

func TestCatalogMeasurementFactsPreserveObservedZero(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixtureWithPower(t, 4096, [6]float64{})
	view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("zero-power catalog=%+v error=%v", view, err)
	}
	row := view.MeasurementFacts[view.Profiles[0].ID].Batches[0]
	for _, value := range []*float64{row.IdleWatts, row.ActiveWatts, row.TotalJoulesPerItem, row.IncrementalJoulesPerItem} {
		if value == nil || *value != 0 {
			t.Fatalf("observed zero became absent: %+v", row)
		}
	}
}
