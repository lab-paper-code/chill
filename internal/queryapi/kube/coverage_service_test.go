package kube

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
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
			for kind := range base.Checked {
				base.Checked[kind] = time.Now().Add(-6 * time.Minute)
			}
			entry := &measurementCoverageEntry{base: base, refreshing: true, refreshed: make(chan struct{})}
			service := &CoverageService{
				MaxRequestBytes: 8 << 20,
				Authorize:       authorizeCoverageNamespace(coverageSizeNamespace),
				cache: measurementCoverageCache{
					namespaces: map[string]*measurementCoverageEntry{coverageSizeNamespace: entry},
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			waiting := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				payload, err := service.Coverage(
					coverageWaitContext{Context: ctx, waiting: waiting}, coverageSizeNamespace,
					queryapi.CoverageQuery{Batches: []int{1, 4}}, false,
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
			service.cache.mu.Lock()
			if fail {
				entry.err = errors.New("source unavailable")
			} else {
				entry.base = coverageSizeBase()
			}
			entry.refreshing = false
			close(entry.refreshed)
			service.cache.mu.Unlock()
			if err := <-result; (err != nil) != fail {
				t.Fatalf("refresh failure=%v, got %v", fail, err)
			}
		})
	}
}

func TestCoverageWithinStaleWindowReturnsWhileRefreshing(t *testing.T) {
	base := coverageSizeBase()
	for kind := range base.Checked {
		base.Checked[kind] = time.Now().Add(-time.Minute)
	}
	entry := &measurementCoverageEntry{base: base, refreshing: true, refreshed: make(chan struct{})}
	service := &CoverageService{
		MaxRequestBytes: 8 << 20,
		Authorize:       authorizeCoverageNamespace(coverageSizeNamespace),
		cache: measurementCoverageCache{
			namespaces: map[string]*measurementCoverageEntry{coverageSizeNamespace: entry},
		},
	}
	payload, err := service.Coverage(
		context.Background(), coverageSizeNamespace, queryapi.CoverageQuery{}, false,
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
	view := queryapi.CoverageCatalog{
		ConfigVersion: "latest-config", Insufficiencies: []queryapi.CoverageCatalogIssue{{Code: "LatestOnlyDiagnostic", Reason: "current reporting"}},
		MeasurementFacts: map[string]profilederivation.MeasurementFacts{"latest-only": {}},
	}
	accepted := queryapi.CoverageCatalog{
		ConfigVersion: "accepted-config", Profiles: []queryapi.CoverageProfile{{ID: "accepted-only"}},
		Insufficiencies: []queryapi.CoverageCatalogIssue{
			{Code: "PreparationUnavailable", Reason: "accepted diagnostic"},
		},
		MeasurementFacts: map[string]profilederivation.MeasurementFacts{"accepted-only": {}},
	}
	service := &CoverageService{
		Reader:          fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build(),
		MaxRequestBytes: 8 << 20,
		Catalog: coverageCatalogFunc(func(context.Context) (queryapi.CoverageCatalogRead, error) {
			return queryapi.CoverageCatalogRead{
				Reporting:       view,
				SourceCheckedAt: time.Now().UTC(),
				Accepted:        &accepted,
			}, nil
		}),
		Inventory: func(context.Context) (queryapi.InventoryView, error) { return base.Inventory, nil },
		Compatibility: func(context.Context) (queryapi.ModelCompatibilityMatrixView, error) {
			return base.Compatibility, nil
		},
	}

	got, err := service.readCoverage(context.Background(), "allowed", nil)
	if err != nil || got == nil {
		t.Fatalf("successful sources discarded the snapshot: snapshot=%v error=%v", got, err)
	}
	if len(got.Checked) != len(queryapi.CoverageSourceKinds) ||
		len(got.Failures) != 0 || len(got.Inventory.Models) == 0 {
		t.Fatalf("incomplete source snapshot: checked=%v failures=%v", got.Checked, got.Failures)
	}
	if !reflect.DeepEqual(got.Catalog.MeasurementFacts, view.MeasurementFacts) ||
		!reflect.DeepEqual(got.Catalog.Insufficiencies, view.Insufficiencies) ||
		got.Catalog.ConfigVersion != "latest-config" || !got.Adopted["accepted-only"] {
		t.Fatalf("coverage mixed accepted facts/diagnostics into current reporting: %+v", got.Catalog)
	}
}

func TestCoverageFreshNamespaceSharedByFiveCallers(t *testing.T) {
	base := &queryapi.CoverageBase{Checked: map[string]time.Time{}, Failures: map[string]error{}}
	for _, kind := range []string{"inventory", "compatibility", "catalog", "BEO", "Run"} {
		base.Checked[kind] = time.Now().UTC()
	}
	service := &CoverageService{
		MaxRequestBytes: 8 << 20,
		Authorize:       authorizeCoverageNamespace("allowed"),
		cache: measurementCoverageCache{
			namespaces: map[string]*measurementCoverageEntry{"allowed": {base: base}},
		},
	}
	// Nil Kubernetes/Query API dependencies make any redundant source I/O fail.
	results := make(chan error, 5)
	for range 5 {
		go func() {
			_, err := service.Coverage(
				context.Background(),
				"allowed",
				queryapi.CoverageQuery{Batches: []int{1, 2}}, false,
			)
			results <- err
		}()
	}
	for range 5 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(base.Checked) != 5 || len(base.Requests.Items) != 0 {
		t.Fatal("query projection mutated base snapshot")
	}
}

func authorizeCoverageNamespace(allowed string) func(string) error {
	return func(namespace string) error {
		if namespace != allowed {
			return errors.New("namespace forbidden")
		}
		return nil
	}
}

func TestCoverageAuthorizesBeforeCacheOrSourceReads(t *testing.T) {
	denied := errors.New("namespace denied")
	service := &CoverageService{Authorize: func(namespace string) error {
		if namespace != "outside" {
			t.Fatalf("namespace changed before authorization: %q", namespace)
		}
		return denied
	}}
	if _, err := service.Coverage(
		context.Background(), "outside", queryapi.CoverageQuery{}, false,
	); !errors.Is(err, denied) || service.cache.namespaces != nil {
		t.Fatalf("authorization was bypassed: error=%v cache=%+v", err, service.cache.namespaces)
	}
}
