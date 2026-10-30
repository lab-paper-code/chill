package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api":
			body = meta.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			version := meta.GroupVersionForDiscovery{GroupVersion: api.GroupVersion.String(), Version: api.GroupVersion.Version}
			body = meta.APIGroupList{Groups: []meta.APIGroup{{Name: api.GroupVersion.Group, Versions: []meta.GroupVersionForDiscovery{version}, PreferredVersion: version}}}
		case "/apis/" + api.GroupVersion.String():
			body = meta.APIResourceList{GroupVersion: api.GroupVersion.String(), APIResources: []meta.APIResource{{Name: "batchenergyoptimizations", Kind: "BatchEnergyOptimization", Namespaced: true, Verbs: meta.Verbs{"get", "list"}}}}
		case "/apis/" + api.GroupVersion.String() + "/namespaces/serving/batchenergyoptimizations":
			if sourceFailed.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(meta.Status{Status: "Failure", Reason: meta.StatusReasonServiceUnavailable, Message: "catalog source unavailable", Code: http.StatusServiceUnavailable})
				return
			}
			body = api.BatchEnergyOptimizationList{TypeMeta: meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimizationList"}, Items: []api.BatchEnergyOptimization{}}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
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
	settings := serving.SchedulerSettings{
		ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
		MaxObservationAge: time.Second, MaxReplicas: 1, MaxInstances: 1,
		CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
		CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
		CapacityMinimumRequests: 1, PlanSearchWorkers: 1, MaxCPUs: 2,
	}
	raw, err := json.Marshal(schedulerConfiguration{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	service := &schedulerService{
		catalog: catalog, costs: costs,
		kube: fake.NewSimpleClientset(&core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "new"}, Data: map[string]string{"config": string(raw)}}),
		opts: options{servingNamespace: "serving", schedulerConfigMap: "scheduler", schedulerConfigKey: "config", maxRequestBytes: 1 << 20},
	}
	adopted := &httpapi.ProfilesView{CatalogView: servingkube.CatalogView{Profiles: []serving.Profile{{ID: "prior-adopted"}}, Revision: "prior"}}
	service.profiles.Store(adopted)
	ctx := context.Background()
	if err := service.refresh(ctx); err == nil || err.Error() != "cost source unavailable" {
		t.Fatalf("cost failure was hidden: %v", err)
	}
	first, err := service.measurementCatalogView(ctx)
	if err != nil || first.SourceCheckedAt.IsZero() || first.SourceError != "" || first.Revision == "prior" ||
		len(first.Profiles) != 0 || first.MeasurementFacts == nil || service.profiles.Load() != adopted {
		t.Fatalf("cost failure hid new verified catalog or changed adoption: %+v error=%v", first, err)
	}
	if err := service.refresh(ctx); err == nil {
		t.Fatal("cost source unexpectedly succeeded")
	}
	second, err := service.measurementCatalogView(ctx)
	if err != nil || !second.SourceCheckedAt.After(first.SourceCheckedAt) || second.Revision != first.Revision {
		t.Fatalf("unchanged source success was not refreshed: %+v error=%v", second, err)
	}
	sourceFailed.Store(true)
	if err := service.refresh(ctx); err == nil {
		t.Fatal("catalog source failure was hidden")
	}
	stale, err := service.measurementCatalogView(ctx)
	if err != nil || stale.SourceError == "" || stale.SourceCheckedAt != second.SourceCheckedAt || stale.Revision != second.Revision || service.profiles.Load() != adopted || objects.rawReads.Load() != 0 {
		t.Fatalf("failed refresh changed retained catalog check/facts/adoption: %+v error=%v reads=%d", stale, err, objects.rawReads.Load())
	}
}
