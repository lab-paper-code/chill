package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type coverageWaitContext struct {
	context.Context
	waiting chan struct{}
}

func (c coverageWaitContext) Done() <-chan struct{} {
	close(c.waiting)
	return c.Context.Done()
}

func TestCoverageExpiredSnapshotWaitsForActiveRefresh(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			base := coverageSizeBase()
			for kind := range base.checked {
				base.checked[kind] = time.Now().Add(-6 * time.Minute)
			}
			entry := &measurementCoverageEntry{base: base, refreshing: true, refreshed: make(chan struct{})}
			service := &measurementService{
				opts: options{servingNamespace: coverageSizeNamespace, maxRequestBytes: 8 << 20},
				coverageCache: measurementCoverageCache{
					namespaces: map[string]*measurementCoverageEntry{coverageSizeNamespace: entry},
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			waiting := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				payload, err := service.coverage(
					coverageWaitContext{Context: ctx, waiting: waiting}, coverageSizeNamespace,
					httpapi.MeasurementCoverageQuery{Batches: []int{1, 4}},
				)
				if err == nil {
					var view struct {
						Freshness string `json:"freshness"`
					}
					err = json.Unmarshal(payload, &view)
					if err == nil && view.Freshness != "fresh" {
						err = errors.New("refresh success returned expired snapshot")
					}
				}
				result <- err
			}()
			select {
			case <-waiting:
			case err := <-result:
				t.Fatalf("returned before active refresh completed: %v", err)
			case <-ctx.Done():
				t.Fatal("request did not wait for refresh")
			}
			service.coverageCache.mu.Lock()
			if fail {
				entry.err = errors.New("source unavailable")
			} else {
				entry.base = coverageSizeBase()
			}
			entry.refreshing = false
			close(entry.refreshed)
			service.coverageCache.mu.Unlock()
			if err := <-result; (err != nil) != fail {
				t.Fatalf("refresh failure=%v, got %v", fail, err)
			}
		})
	}
}

func TestCoverageWithinStaleWindowReturnsWhileRefreshing(t *testing.T) {
	base := coverageSizeBase()
	for kind := range base.checked {
		base.checked[kind] = time.Now().Add(-time.Minute)
	}
	entry := &measurementCoverageEntry{base: base, refreshing: true, refreshed: make(chan struct{})}
	service := &measurementService{
		opts: options{servingNamespace: coverageSizeNamespace, maxRequestBytes: 8 << 20},
		coverageCache: measurementCoverageCache{
			namespaces: map[string]*measurementCoverageEntry{coverageSizeNamespace: entry},
		},
	}
	payload, err := service.coverage(context.Background(), coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Freshness string `json:"freshness"`
	}
	if err := json.Unmarshal(payload, &view); err != nil || view.Freshness != "stale" {
		t.Fatalf("in-flight refresh hid usable stale snapshot: %s, %v", payload, err)
	}
}

func TestReadCoverageReturnsSnapshotWhenAllSixSourcesSucceed(t *testing.T) {
	base := coverageSizeBase()
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch r.URL.Path {
		case "/api/v1/inventory":
			body = base.inventory
		case "/api/v1/model-compatibility-matrix":
			body = base.compatibility
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(console.Close)
	origin, err := url.Parse(console.URL)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &servingkube.Catalog{}
	view, err := catalog.Refresh(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := &schedulerService{catalog: catalog}
	scheduler.catalogRead.Store(&httpapi.ProfilesView{CatalogView: view})
	service := &measurementService{
		client:  fake.NewClientBuilder().WithScheme(measurementScheme(t)).Build(),
		console: origin, http: console.Client(), scheduler: scheduler,
		opts: options{maxRequestBytes: 8 << 20},
	}
	got, err := service.readCoverage(context.Background(), "allowed", nil)
	if err != nil || got == nil {
		t.Fatalf("six successful sources discarded the snapshot: snapshot=%v error=%v", got, err)
	}
	if len(got.checked) != 6 || len(got.failures) != 0 || len(got.inventory.Models) == 0 {
		t.Fatalf("incomplete source snapshot: checked=%v failures=%v", got.checked, got.failures)
	}
}
