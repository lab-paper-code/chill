package kube

import (
	"context"
	"errors"
	"io"
	"math"
	"sync/atomic"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type countedCatalogObjects struct {
	objects catalogEvidenceObjects
	reads   atomic.Int64
}

func (c *countedCatalogObjects) OpenLocation(ctx context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	c.reads.Add(1)
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
	if counted.reads.Load() != 14 {
		t.Fatalf("facts added raw I/O: got %d reads, want result+plan+6 execution+6 raw", counted.reads.Load())
	}
	id := first.Profiles[0].ID
	facts := first.MeasurementFacts[id]
	if len(facts.Batches) != 1 {
		t.Fatal(facts)
	}
	row := facts.Batches[0]
	assertCatalogMeasurementBatchFacts(t, row)
	*row.P95Milliseconds = 999
	facts.Condition.PlannedBatchSizes[0] = 999
	second, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || counted.reads.Load() != 14 || !second.SourceCheckedAt.After(first.SourceCheckedAt) || second.Revision != first.Revision {
		t.Fatalf("unchanged refresh did not check source without raw I/O: %+v %v reads=%d", second, err, counted.reads.Load())
	}
	if *second.MeasurementFacts[id].Batches[0].P95Milliseconds != 2 || second.MeasurementFacts[id].Condition.PlannedBatchSizes[0] == 999 {
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

func assertCatalogMeasurementBatchFacts(t *testing.T, row profilederivation.MeasurementBatchFacts) {
	t.Helper()
	if row.LatencySampleCount != 540 || row.ActiveTrialCount != 3 || row.IdleTrialCount != 3 ||
		row.CompletedItems != 540 || row.CompletedCalls != 540 {
		t.Fatalf("raw-derived repetition/call counts changed: %+v", row)
	}
	for _, quantity := range []struct {
		name  string
		value *float64
		want  float64
	}{
		{"client p95", row.P95Milliseconds, 2},
		{"idle W", row.IdleWatts, 4},
		{"active W", row.ActiveWatts, 6},
		{"total J/item", row.TotalJoulesPerItem, 3},
		{"incremental J/item", row.IncrementalJoulesPerItem, 1},
	} {
		if quantity.value == nil || math.Abs(*quantity.value-quantity.want) > 1e-9 {
			t.Errorf("%s = %v, want %g", quantity.name, quantity.value, quantity.want)
		}
	}
	if row.Confidence == nil || row.Confidence.HalfWidth != 0 || row.Confidence.Level != .95 ||
		len(row.Quality) != 6 || len(row.Sources) != 13 {
		t.Fatalf("raw-derived confidence/capture lineage changed: %+v", row)
	}
	if row.PowerSource.Kind == "" || !row.Quality[0].SourceTimestampAbsent || row.Quality[0].PolicyPassed != nil {
		t.Fatalf("source/capture facts invented a quality policy: %+v", row)
	}
	for _, quality := range row.Quality {
		if quality.PolicyPassed != nil || quality.PolicyReason != "PolicyUnset" {
			t.Fatalf("missing frozen power policy was not distinguished: %+v", quality)
		}
	}
}

func TestCatalogMeasurementFactsPreserveObservedZero(t *testing.T) {
	// The stored estimator requires positive wall energy; equal positive active
	// and idle power gives a valid observed zero incremental energy and CI.
	catalog, source, _, _ := catalogEvidenceFixtureWithPower(t, 4096, [6]float64{4, 4, 4, 4, 4, 4})
	view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("zero-power catalog=%+v error=%v", view, err)
	}
	row := view.MeasurementFacts[view.Profiles[0].ID].Batches[0]
	if row.IncrementalJoulesPerItem == nil || *row.IncrementalJoulesPerItem != 0 ||
		row.Confidence == nil || row.Confidence.HalfWidth != 0 {
		t.Fatalf("observed zero incremental energy/CI became absent: %+v", row)
	}
	if row.IdleWatts == nil || *row.IdleWatts != 4 || row.ActiveWatts == nil || *row.ActiveWatts != 4 ||
		row.TotalJoulesPerItem == nil || *row.TotalJoulesPerItem != 2 {
		t.Fatalf("zero incremental energy changed positive wall measurement facts: %+v", row)
	}
}
