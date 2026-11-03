package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
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
					httpapi.MeasurementCoverageQuery{Batches: []int{1, 4}}, false,
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
	payload, err := service.coverage(
		context.Background(), coverageSizeNamespace, httpapi.MeasurementCoverageQuery{}, false,
	)
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

func TestReadCoverageReturnsSnapshotWhenAllSourcesSucceed(t *testing.T) {
	base := coverageSizeBase()
	catalog := &servingkube.Catalog{}
	view, err := catalog.Refresh(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	view.Insufficiencies = []servingkube.CatalogIssue{{Code: "LatestOnlyDiagnostic", Reason: "current reporting"}}
	view.MeasurementFacts = map[string]serving.MeasurementFacts{"latest-only": {}}
	accepted := servingkube.ProjectCatalogSnapshot(servingkube.CatalogView{
		Profiles:         []serving.Profile{{ID: "accepted-only"}},
		Insufficiencies:  []servingkube.CatalogIssue{{Code: "PreparationUnavailable", Reason: "accepted diagnostic"}},
		MeasurementFacts: map[string]serving.MeasurementFacts{"accepted-only": {}},
	}, "accepted-config", nil)
	service := &measurementService{
		client: fake.NewClientBuilder().WithScheme(measurementScheme(t)).Build(),
		opts:   options{maxRequestBytes: 8 << 20},
		catalog: func(context.Context) (servingkube.CatalogRead, error) {
			return servingkube.CatalogRead{
				CatalogSnapshot: servingkube.ProjectCatalogSnapshot(view, "latest-config", nil), Accepted: &accepted,
			}, nil
		},
		inventory: func(context.Context) (queryapi.InventoryView, error) { return base.inventory, nil },
		compatibility: func(context.Context) (queryapi.ModelCompatibilityMatrixView, error) {
			return base.compatibility, nil
		},
	}

	got, err := service.readCoverage(context.Background(), "allowed", nil)
	if err != nil || got == nil {
		t.Fatalf("successful sources discarded the snapshot: snapshot=%v error=%v", got, err)
	}
	if len(got.checked) != len(measurementCoverageSourceKinds) ||
		len(got.failures) != 0 || len(got.inventory.Models) == 0 {
		t.Fatalf("incomplete source snapshot: checked=%v failures=%v", got.checked, got.failures)
	}
	if !reflect.DeepEqual(got.catalog.MeasurementFacts, view.MeasurementFacts) ||
		!reflect.DeepEqual(got.catalog.Insufficiencies, view.Insufficiencies) ||
		got.catalog.ConfigVersion != "latest-config" || !got.adopted["accepted-only"] {
		t.Fatalf("coverage mixed accepted facts/diagnostics into current reporting: %+v", got.catalog)
	}
}
