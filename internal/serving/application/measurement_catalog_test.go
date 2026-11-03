package application

import (
	"context"
	"encoding/json"
	"errors"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

type unavailableCatalogCosts struct {
	rawReads atomic.Int32
}

func (c *unavailableCatalogCosts) Open(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	c.rawReads.Add(1)
	return nil, errors.New("unexpected raw read")
}
func (c *unavailableCatalogCosts) OpenLocation(context.Context, evidencestore.ObjectLocation) (io.ReadCloser, error) {
	c.rawReads.Add(1)
	return nil, errors.New("unexpected raw read")
}
func (*unavailableCatalogCosts) List(context.Context, string) ([]evidencestore.ObjectIdentity, error) {
	return nil, errors.New("cost source unavailable")
}

func TestCostFailurePublishesVerifiedCatalogWithoutChangingAdoption(t *testing.T) {
	var sourceFailed atomic.Bool
	server := newMeasurementCatalogSourceServer(t, &sourceFailed)
	defer server.Close()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, err := servingkube.NewStore(&rest.Config{Host: server.URL}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	objects := &unavailableCatalogCosts{}
	catalog, err := servingkube.NewCatalog(store, objects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	costs, err := servingkube.NewCostEvidence(store, objects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	settings := withSchedulerEstimateFixture(serving.SchedulerSettings{
		ObservationWindow: time.Second,
		ControlInterval:   time.Second, ComparisonPeriod: time.Second,
		MaxObservationAge: time.Second, MaxReplicas: 1, MaxInstances: 1,
		CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
		CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
		CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
	})
	raw, err := json.Marshal(SchedulerConfiguration{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	service := &SchedulerService{
		catalog: catalog, costs: costs,
		kube: fake.NewSimpleClientset(&core.ConfigMap{
			ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "new"},
			Data:       map[string]string{"config": string(raw)},
		}),
		opts: Options{
			ServingNamespace: "serving", SchedulerConfigMap: "scheduler",
			SchedulerConfigKey: "config", MaxRequestBytes: 1 << 20,
		},
	}
	acceptedEnergy := 3.0
	adopted := &ProfilesView{
		ConfigVersion: "accepted-config",
		CatalogView: servingkube.CatalogView{
			Profiles: []serving.Profile{{ID: "accepted-only", Batches: []serving.BatchProfile{{Batch: 1}}}},
			Revision: "prior",
			MeasurementFacts: map[string]serving.MeasurementFacts{"accepted-only": {
				PointPolicy: profilederivation.PointPolicy{MinimumTrials: 1},
				Batches:     []profilederivation.MeasurementBatchFacts{{Batch: 1, TotalJoulesPerItem: &acceptedEnergy}},
			}},
			Insufficiencies: []servingkube.CatalogIssue{
				{Code: preparationUnavailable, Reason: "accepted preparation failed"},
			},
		},
		Preparations: map[string]servingstate.PrepareSpec{"accepted-only": {Name: "accepted-preparation"}},
		Occupancy: []servingkube.OccupancySelection{{
			Node:      serving.NodeReference{Name: "node", UID: "node-uid"},
			PowerMode: "fixed", Model: "model", ProfileID: "accepted-only", Batch: 2,
			Evidence: evidencestore.ObjectIdentity{
				Store: "evidence",
				Key:   servingkube.DispatchOccupancyPrefix + "node-uid/dom-uid.json",
				Size:  100,
			},
		}},
	}
	service.Profiles.Store(adopted)
	ctx := context.Background()
	if err := service.Refresh(ctx); err == nil || err.Error() != "cost source unavailable" {
		t.Fatalf("cost failure was hidden: %v", err)
	}
	published := *service.catalogRead.Load()
	published.Insufficiencies = []servingkube.CatalogIssue{{Code: "LatestOnlyDiagnostic", Reason: "current reporting"}}
	published.MeasurementFacts = map[string]serving.MeasurementFacts{"latest-only": {}}
	service.catalogRead.Store(&published)
	first, err := service.measurementCatalogView(ctx)
	if err != nil || first.SourceCheckedAt.IsZero() || first.SourceError != "" || first.Revision == "prior" ||
		len(first.Profiles) != 0 || first.MeasurementFacts == nil || service.Profiles.Load() != adopted {
		t.Fatalf("cost failure hid new verified catalog or changed adoption: %+v error=%v", first, err)
	}
	assertAcceptedCatalogRead(t, service, adopted, first)
	if err := service.Refresh(ctx); err == nil {
		t.Fatal("cost source unexpectedly succeeded")
	}
	second, err := service.measurementCatalogView(ctx)
	if err != nil || !second.SourceCheckedAt.After(first.SourceCheckedAt) || second.Revision != first.Revision {
		t.Fatalf("unchanged source success was not refreshed: %+v error=%v", second, err)
	}
	sourceFailed.Store(true)
	if err := service.Refresh(ctx); err == nil {
		t.Fatal("catalog source failure was hidden")
	}
	stale, err := service.measurementCatalogView(ctx)
	if err != nil || stale.SourceError == "" || stale.SourceCheckedAt != second.SourceCheckedAt ||
		stale.Revision != second.Revision || service.Profiles.Load() != adopted || objects.rawReads.Load() != 0 {
		t.Fatalf("failed refresh changed retained catalog check/facts/adoption: %+v error=%v reads=%d",
			stale, err, objects.rawReads.Load())
	}
	assertAcceptedCatalogRead(t, service, adopted, stale)
}

func assertAcceptedCatalogRead(
	t *testing.T, service *SchedulerService, adopted *ProfilesView, latest ProfilesView,
) {
	t.Helper()
	read, err := service.ReadCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	var queryRead servingkube.CatalogRead
	if err := json.Unmarshal(wire, &queryRead); err != nil {
		t.Fatal(err)
	}
	accepted := queryRead.Accepted
	if accepted == nil {
		t.Fatalf("accepted snapshot disappeared: %s", wire)
	}
	if !reflect.DeepEqual(accepted.Occupancy, adopted.Occupancy) || len(queryRead.Occupancy) != 0 {
		t.Fatalf("selected occupancy mixed with reporting or lost accepted provenance: %s", wire)
	}
	if accepted.ConfigVersion != adopted.ConfigVersion || len(accepted.Profiles) != 1 ||
		accepted.Profiles[0].ID != adopted.Profiles[0].ID ||
		accepted.Preparations[accepted.Profiles[0].ID].Name != "accepted-preparation" {
		t.Fatalf("query catalog mixed verified and accepted snapshots: %s", wire)
	}
	if !reflect.DeepEqual(accepted.Insufficiencies, adopted.Insufficiencies) ||
		!reflect.DeepEqual(accepted.ReuseChecks, adopted.ReuseChecks) ||
		!reflect.DeepEqual(accepted.CatalogView().MeasurementFacts, adopted.MeasurementFacts) {
		t.Fatalf("accepted diagnostics/reuse/reporting facts disappeared: %s", wire)
	}
	if queryRead.ConfigVersion != latest.ConfigVersion || queryRead.SourceCheckedAt != latest.SourceCheckedAt ||
		!reflect.DeepEqual(queryRead.Insufficiencies, latest.Insufficiencies) ||
		!reflect.DeepEqual(queryRead.CatalogView().MeasurementFacts, latest.MeasurementFacts) {
		t.Fatalf("accepted snapshot replaced independently refreshed reporting: %s", wire)
	}
}

func newMeasurementCatalogSourceServer(t *testing.T, sourceFailed *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api":
			body = meta.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			version := meta.GroupVersionForDiscovery{
				GroupVersion: api.GroupVersion.String(), Version: api.GroupVersion.Version,
			}
			body = meta.APIGroupList{Groups: []meta.APIGroup{{
				Name: api.GroupVersion.Group, Versions: []meta.GroupVersionForDiscovery{version}, PreferredVersion: version,
			}}}
		case "/apis/" + api.GroupVersion.String():
			body = meta.APIResourceList{
				GroupVersion: api.GroupVersion.String(),
				APIResources: []meta.APIResource{{
					Name: "batchenergyoptimizations", Kind: "BatchEnergyOptimization", Namespaced: true,
					Verbs: meta.Verbs{"get", "list"},
				}},
			}
		case "/apis/" + api.GroupVersion.String() + "/namespaces/serving/batchenergyoptimizations":
			if sourceFailed.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(meta.Status{
					Status: "Failure", Reason: meta.StatusReasonServiceUnavailable,
					Message: "catalog source unavailable", Code: http.StatusServiceUnavailable,
				})
				return
			}
			body = api.BatchEnergyOptimizationList{
				TypeMeta: meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimizationList"},
				Items:    []api.BatchEnergyOptimization{},
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
}
