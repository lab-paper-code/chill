package kube

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
)

type delayedCatalogObjects struct {
	objects catalogEvidenceObjects
	active  atomic.Int64
	peak    atomic.Int64
}

func (s *delayedCatalogObjects) OpenLocation(ctx context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for peak := s.peak.Load(); active > peak; peak = s.peak.Load() {
		if s.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	// Earlier input finishes later, so completion order cannot choose diagnostics.
	delay := 2 * time.Millisecond
	if strings.HasPrefix(location.Key, "trial-0-") {
		delay = 10 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return s.objects.OpenLocation(ctx, location)
	}
}

func TestCatalogCompletedTrialsMatchSerialAndBoundIO(t *testing.T) {
	catalog, source, objects, _ := catalogEvidenceFixture(t, 4096)
	ctx := context.Background()
	measurement := &edge.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, measurement); err != nil {
		t.Fatal(err)
	}
	result, err := campaign.DecodeResultRecord(objects[apiObjectIdentity(measurement.Status.Result).Location()])
	if err != nil {
		t.Fatal(err)
	}
	// More than the limit also checks errgroup's bound independently of windowing.
	references := result.Spec().CompletedTrials
	references = append(references, references...)
	delayed := &delayedCatalogObjects{objects: objects}
	catalog.loader, err = profilerevidence.NewLoader(delayed, catalog.maxObjectBytes)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	serial := make([]catalogTrial, len(references))
	for index, reference := range references {
		serial[index] = catalog.readCompletedTrial(ctx, measurement, reference)
		if serial[index].err != nil {
			t.Fatal(serial[index].err)
		}
	}
	serialTime := time.Since(start)
	delayed.peak.Store(0)
	start = time.Now()
	parallel := catalog.readCompletedTrials(ctx, measurement, references)
	parallelTime := time.Since(start)
	if !reflect.DeepEqual(serial, parallel) {
		t.Fatal("parallel verified records differ from ordered serial records")
	}
	if peak := delayed.peak.Load(); peak <= 1 || peak > catalogTrialConcurrency {
		t.Fatalf("peak concurrent store reads = %d, want 2..%d", peak, catalogTrialConcurrency)
	}
	t.Logf("delayed store serial=%s parallel=%s speedup=%.2fx peak=%d", serialTime, parallelTime, float64(serialTime)/float64(parallelTime), delayed.peak.Load())
	first, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(first.Profiles) != 1 {
		t.Fatalf("parallel profile: %+v %v", first, err)
	}
	clear(catalog.current)
	second, err := catalog.Refresh(ctx, []string{source.Namespace})
	second.SourceCheckedAt = first.SourceCheckedAt
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("profile, diagnostics, facts or lineage order changed: %v", err)
	}
}

func TestCatalogParallelFailuresPreserveOrderedDiagnostics(t *testing.T) {
	for _, corruption := range []string{"size", "strict execution", "strict raw"} {
		t.Run(corruption, func(t *testing.T) {
			catalog, source, objects, _ := catalogEvidenceFixture(t, 4096)
			for _, trial := range []string{"trial-0", "trial-1"} {
				location := evidencestore.ObjectLocation{Store: "evidence", Key: trial + "-execution"}
				if corruption == "strict raw" {
					location.Key = trial + "-raw"
				}
				if corruption == "size" {
					objects[location] = append(objects[location], ' ')
				} else {
					objects[location] = bytes.Replace(objects[location], []byte(`"schemaVersion"`), []byte(`"unknownFieldX"`), 1)
				}
			}
			delayed := &delayedCatalogObjects{objects: objects}
			var err error
			catalog.loader, err = profilerevidence.NewLoader(delayed, catalog.maxObjectBytes)
			if err != nil {
				t.Fatal(err)
			}
			view, err := catalog.Refresh(context.Background(), []string{source.Namespace})
			if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || len(catalog.current) != 0 {
				t.Fatalf("failed trial published partial profile: %+v %v", view, err)
			}
			issue := view.Insufficiencies[0]
			if issue.Code != invalidEvidenceCode || !strings.Contains(issue.Reason, "trial-0") {
				t.Fatalf("completion order changed first failure: %+v", issue)
			}
			if corruption != "size" && !strings.Contains(issue.Reason, "unknown field") {
				t.Fatalf("strict decoding lost: %+v", issue)
			}
		})
	}
}

func TestCatalogParallelCancellationCannotPublishPartialView(t *testing.T) {
	catalog, source, objects, _ := catalogEvidenceFixture(t, 4096)
	var err error
	catalog.loader, err = profilerevidence.NewLoader(&delayedCatalogObjects{objects: objects}, catalog.maxObjectBytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if !errors.Is(err, context.DeadlineExceeded) || len(view.Profiles) != 0 || len(catalog.current) != 0 {
		t.Fatalf("cancelled refresh published partial view: %+v %v", view, err)
	}
}
