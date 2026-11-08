package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	appsv1 "k8s.io/api/apps/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// Remote measurement evidence uses the fixture's separate real catalog source.
// These empty live lists let the real owner loop capture and publish decisions.
func installPolicyControlSources(t *testing.T, service *SchedulerService) {
	t.Helper()
	resources := map[string][]meta.APIResource{
		"apps/v1": {
			{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "replicasets", Kind: "ReplicaSet", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
		},
		edge.GroupVersion.String(): {
			{Name: "deviceclasses", Kind: "DeviceClass", Verbs: meta.Verbs{"get", "list"}},
			{Name: "batchenergyoptimizations", Kind: "BatchEnergyOptimization", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "modelloadedidlebaselines", Kind: "ModelLoadedIdleBaseline", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "profilingruns", Kind: "ProfilingRun", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "powermodetransitioncosts", Kind: "PowerModeTransitionCost", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "operationcostmeasurements", Kind: "OperationCostMeasurement", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "dispatchoccupancymeasurements", Kind: "DispatchOccupancyMeasurement", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api" {
			_ = json.NewEncoder(w).Encode(meta.APIVersions{Versions: []string{"v1"}})
			return
		}
		if r.URL.Path == "/apis" {
			groups := meta.APIGroupList{}
			for groupVersion := range resources {
				parts := strings.Split(groupVersion, "/")
				version := meta.GroupVersionForDiscovery{GroupVersion: groupVersion, Version: parts[1]}
				groups.Groups = append(groups.Groups, meta.APIGroup{Name: parts[0], Versions: []meta.GroupVersionForDiscovery{version}, PreferredVersion: version})
			}
			_ = json.NewEncoder(w).Encode(groups)
			return
		}
		if r.URL.Path == "/api/v1" {
			_ = json.NewEncoder(w).Encode(meta.APIResourceList{GroupVersion: "v1"})
			return
		}
		for groupVersion, declarations := range resources {
			base := "/apis/" + groupVersion
			if r.URL.Path == base {
				_ = json.NewEncoder(w).Encode(meta.APIResourceList{GroupVersion: groupVersion, APIResources: declarations})
				return
			}
			if !strings.HasPrefix(r.URL.Path, base+"/") {
				continue
			}
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			for _, declaration := range declarations {
				if declaration.Name == name {
					_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": groupVersion, "kind": declaration.Kind + "List", "items": []any{}})
					return
				}
			}
		}
		t.Errorf("unexpected live control request: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	scheme := service.store.Client().Scheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, err := servingkube.NewStore(&rest.Config{Host: server.URL}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := servingkube.NewObserver(service.cluster, store, service.kube, service.execution, &sdk.Client{}, servingkube.ObserverConfig{
		Namespace: "serving", RuntimeImage: service.observer.CurrentRuntimeImage,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	controller, err := servingkube.NewSchedulerController(service.cluster, store, service.kube, service.execution, observer,
		service.operations, service.observationLog, servingkube.SchedulerControllerConfig{Settings: service.Controller.Settings()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	service.observer, service.Controller = observer, controller
}

func assertPolicyControlUsesSettings(t *testing.T, service *SchedulerService, settings serving.SchedulerSettings) {
	t.Helper()
	decisions := make(chan serving.SchedulerDecision, 1)
	service.live = &Live{Decision: func(decision serving.SchedulerDecision, _ serving.SchedulerSnapshot) {
		select {
		case decisions <- decision:
		default:
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	service.ctx = ctx
	service.opts.RequestTimeout = time.Second
	service.Start()
	defer func() {
		cancel()
		service.wg.Wait()
		service.live = nil
	}()
	select {
	case decision := <-decisions:
		if !reflect.DeepEqual(decision.Settings, settings) {
			t.Fatal("control owner used a stale policy while source preparation was held")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("control owner waited for remote preparation before its next decision")
	}
}
