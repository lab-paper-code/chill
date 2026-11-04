package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/application"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func schedulerTestConfiguration() application.SchedulerConfiguration {
	return application.SchedulerConfiguration{
		Settings: serving.SchedulerSettings{
			DefaultOperationCost: map[string]serving.OperationCostValue{
				"prepare": {Seconds: 852, Joules: 36371},
				"remove":  {Seconds: 13, Joules: 1014},
				"power":   {Seconds: 166, Joules: 2229},
			},
			DefaultIdlePower:  &serving.IdlePowerDefault{NodeWatts: 54, PerInstanceWatts: 8},
			ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
			MaxObservationAge: time.Second, MaxReplicas: 1,
			MaxInstances: 5, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
			CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
			CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
		},
		Reserved: map[string]serving.SchedulerAllocation{"rpi": {MemoryBytes: 1024}},
	}
}

func schedulerTestDependencies(t *testing.T, config application.SchedulerConfiguration) application.Dependencies {
	t.Helper()
	ctx := context.Background()
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	execution, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := execution.Shutdown(shutdownCtx); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// No evidence producer is needed: every native read returns NotFound.
	apiServer := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(apiServer.Close)
	store, err := servingkube.NewStore(&rest.Config{Host: apiServer.URL}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{
			Namespace: "serving", Name: "scheduler", ResourceVersion: "1", Labels: map[string]string{"operator": "kept"},
		},
		Data: map[string]string{"scheduler.json": string(raw), "unrelated": "operator value"},
	})
	cluster := servingkube.NewClusterState(store, kube, "serving")
	observer, err := servingkube.NewObserver(cluster, store, kube, execution, &sdk.Client{},
		servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	operations, err := servingops.NewOperations(cluster, store, kube, execution, observer, &sdk.Client{},
		servingops.OperationConfig{Namespace: "serving", PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	objects := &stores3.Store{}
	catalog, err := servingkube.NewCatalog(store, objects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return application.Dependencies{
		Cluster: cluster, Kube: kube, Store: store, Execution: execution, Observer: observer, Objects: objects,
		Catalog: catalog, Operations: operations, ObservationLog: observationLog,
		Options: application.Options{ServingNamespace: "serving", SchedulerConfigMap: "scheduler",
			SchedulerConfigKey: "scheduler.json",
			ProfileNamespaces:  []string{"measurements"}, MaxRequestBytes: 1 << 20, MaxEvidenceBytes: 1024}}
}
