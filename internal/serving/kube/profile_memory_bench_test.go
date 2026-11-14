package kube

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const publicationRecords = 1200000

func publicationCatalog(t testing.TB, profiles int) *Catalog {
	t.Helper()
	result := catalogSessionResult(t)
	result.Batches = nil
	result.Condition.Firstfloor.MaxBatchSize = 128
	result.Condition.Actor.MaxBatchSize = 128
	result.Condition.Actor.BatchSizes = nil
	result.Condition.Actor.WarmupBatchSizes = nil
	for size := 1; size <= 128; size *= 2 {
		rate := float64(size) / (10 * time.Millisecond).Seconds()
		batch := derivation.SessionBatch{Batch: size, PeakMemoryBytes: 32 << 20, MemoryKind: "peak", JoulesPerInput: *catalogTestSlope("0.2"), SaturationInputRate: &rate}
		batch.ServiceNanos = make([]int64, publicationRecords/8)
		batch.PrecedingIdleNanos = make([]int64, publicationRecords/8)
		for i := range batch.ServiceNanos {
			batch.ServiceNanos[i] = 10000000 + int64(i%1000)
			if i%3 == 0 {
				batch.PrecedingIdleNanos[i] = 1000000000 + int64(i)
			}
			if i%3 == 1 {
				batch.DispatchOccupancyNanos = append(batch.DispatchOccupancyNanos, 100000)
			}
		}
		result.Batches = append(result.Batches, batch)
		result.Condition.Actor.BatchSizes = append(result.Condition.Actor.BatchSizes, size)
		result.Condition.Actor.WarmupBatchSizes = append(result.Condition.Actor.WarmupBatchSizes, size)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	identity := evidencestore.ObjectIdentity{Store: "evidence", Key: "session/result.json", Size: int64(len(payload))}
	objects := make([]client.Object, profiles)
	for i := range objects {
		objects[i] = catalogSessionCampaign(fmt.Sprintf("profile-%d", i), types.UID(fmt.Sprintf("profile-uid-%d", i)),
			&edge.ProfilingRunEvidenceReference{Store: identity.Store, Key: identity.Key, Size: identity.Size}, time.Now())
	}
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(&Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()},
		catalogEvidenceObjects{identity.Location(): payload}, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func publicationObserver(t testing.TB) *Observer {
	t.Helper()
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: time.Hour, Writer: managerArchive{t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rt.Shutdown(context.Background())
		_ = log.Close(context.Background())
	})
	return &Observer{observerState: new(observerState), runtime: rt}
}

func TestProfilePublicationMemory(t *testing.T) {
	catalog := publicationCatalog(t, 1)
	observer := publicationObserver(t)
	goruntime.GC()
	// Two collections also release the session encoder's sync.Pool buffers,
	// which are fixture setup rather than retained publication storage.
	goruntime.GC()
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	view, err := catalog.Refresh(context.Background(), []string{"chill-system"})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("profiles=%d err=%v", len(view.Profiles), err)
	}
	for _, batch := range view.Profiles[0].Batches {
		batch.ServiceDistribution.Prepare(95)
	}
	observer.SetProfiles(view.Profiles)
	goruntime.GC()
	goruntime.GC()
	goruntime.ReadMemStats(&after)
	bytesPerRecord := float64(after.HeapAlloc-before.HeapAlloc) / publicationRecords
	t.Logf("published records: %.3f retained bytes/record including p95 tables", bytesPerRecord)
	if bytesPerRecord > 85 {
		t.Fatalf("published records retain %.3f bytes/record", bytesPerRecord)
	}
	var warm CatalogView
	allocs := testing.AllocsPerRun(3, func() {
		warm, err = catalog.Refresh(context.Background(), []string{"chill-system"})
		if err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 2000 {
		t.Fatalf("unchanged refresh allocates per record: %.0f", allocs)
	}
	if &warm.Profiles[0].Batches[0].ServiceSamples[0] != &view.Profiles[0].Batches[0].ServiceSamples[0] {
		t.Fatal("refresh copied records")
	}
	goruntime.KeepAlive(catalog)
	goruntime.KeepAlive(view)
	goruntime.KeepAlive(observer)
}

func BenchmarkProfilePublication(b *testing.B) {
	catalog := publicationCatalog(b, 20)
	observer := publicationObserver(b)
	view, err := catalog.Refresh(context.Background(), []string{"chill-system"})
	if err != nil || len(view.Profiles) != 20 {
		b.Fatalf("profiles=%d err=%v", len(view.Profiles), err)
	}
	observer.SetProfiles(view.Profiles)
	b.Run("UnchangedRefresh20x1200000", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			view, err = catalog.Refresh(context.Background(), []string{"chill-system"})
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("SetProfiles20x1200000", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			observer.SetProfiles(view.Profiles)
		}
	})
}
