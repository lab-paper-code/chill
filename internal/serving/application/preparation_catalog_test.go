package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	projection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	"github.com/lab-paper-code/chill/test/fixtures"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	kubetesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Before the fix, the failed pass accepted an empty preparation map and froze
// its source signature. The healthy pass then skipped materialization forever.
func TestRefreshRecoversPreparationsAfterTransientMaterialization(t *testing.T) {
	for _, boundary := range []string{"node", "declaration"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newPreparationCatalogFixture(t)
			fixture.fail(boundary, true)
			if err := fixture.service.Refresh(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expired materialization was not a refresh failure: %v", err)
			}
			failed, err := fixture.service.ReadCatalog(context.Background())
			if err != nil || failed.Accepted != nil || len(failed.Profiles) != 1 {
				t.Fatalf("failed materialization accepted a partial catalog or lost verified reporting: %+v err=%v", failed, err)
			}
			fixture.fail(boundary, false)
			if err := fixture.service.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			healthy := fixture.prepared(t)
			if healthy.Revision != failed.Revision || healthy.ConfigVersion != failed.ConfigVersion {
				t.Fatal("recovery changed the measurement or configuration identity")
			}
		})
	}
}

func TestRefreshPreparationFailurePreservesAcceptedCatalog(t *testing.T) {
	for _, boundary := range []string{"node", "declaration"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newPreparationCatalogFixture(t)
			ctx := context.Background()
			if err := fixture.service.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			before := fixture.prepared(t)
			accepted := fixture.service.Profiles.Load()
			configuration := fixture.service.configuration.Load()
			cm, err := fixture.service.kube.CoreV1().ConfigMaps("serving").Get(ctx, "scheduler", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			// A new config revision forces adoption even on the old sticky path.
			// Its contents and all measured source identities remain unchanged.
			cm.ResourceVersion = "2"
			if _, err := fixture.service.kube.CoreV1().ConfigMaps("serving").Update(ctx, cm, meta.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			fixture.fail(boundary, true)
			if err := fixture.service.Refresh(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("failed required read was accepted: %v", err)
			}
			after, err := fixture.service.ReadCatalog(ctx)
			if err != nil || !reflect.DeepEqual(after.Accepted, before.Accepted) ||
				fixture.service.Profiles.Load() != accepted || fixture.service.configuration.Load() != configuration {
				t.Fatalf("global preparation failure replaced accepted state: before=%+v after=%+v err=%v", before.Accepted, after.Accepted, err)
			}
			fixture.fail(boundary, false)
			if err := fixture.service.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			recovered := fixture.prepared(t)
			if recovered.Accepted.ConfigVersion != cm.ResourceVersion || recovered.Revision != before.Revision {
				t.Fatal("healthy refresh did not accept the pending revision with unchanged measurements")
			}
		})
	}
}

func TestRefreshRecoversPreparationsAfterCompatibilityRestored(t *testing.T) {
	for _, guard := range []string{"nodeAbsent", "nodeUID", "declaration"} {
		t.Run(guard, func(t *testing.T) {
			fixture := newPreparationCatalogFixture(t)
			ctx := context.Background()
			var restore func()
			if guard == "nodeAbsent" {
				node, err := fixture.service.kube.CoreV1().Nodes().Get(ctx, fixture.profile.NodeName, meta.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.service.kube.CoreV1().Nodes().Delete(ctx, node.Name, meta.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					node.ResourceVersion = ""
					if _, err := fixture.service.kube.CoreV1().Nodes().Create(ctx, node, meta.CreateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
			} else if guard == "nodeUID" {
				node, err := fixture.service.kube.CoreV1().Nodes().Get(ctx, fixture.profile.NodeName, meta.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				node.UID = "recreated-node-uid"
				if _, err := fixture.service.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					node.UID = types.UID(fixture.profile.Condition.NodeUID)
					if _, err := fixture.service.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				facts := &core.ConfigMap{}
				if err := fixture.declarations.Get(ctx, types.NamespacedName{Namespace: "serving", Name: "execution-facts"}, facts); err != nil {
					t.Fatal(err)
				}
				original := facts.Data["execution-facts.yaml"]
				// The source is readable, but no declaration covers this execution.
				facts.Data["execution-facts.yaml"] = `{"schemaVersion":"chill.dacs.io/profiler-execution-facts.v1alpha8","metricObserverImage":"registry/runtime:current","declarations":[]}`
				if err := fixture.declarations.Update(ctx, facts); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					facts.Data["execution-facts.yaml"] = original
					if err := fixture.declarations.Update(ctx, facts); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := fixture.service.Refresh(ctx); err != nil {
				t.Fatalf("local incompatibility failed the complete refresh: %v", err)
			}
			partial, err := fixture.service.ReadCatalog(ctx)
			if err != nil || partial.Accepted == nil || len(partial.Accepted.Profiles) != 1 ||
				len(partial.Accepted.Preparations) != 0 {
				t.Fatalf("incompatible preparation became eligible: %+v err=%v", partial, err)
			}
			found := false
			for _, issue := range partial.Accepted.Insufficiencies {
				if issue.Code == preparationUnavailable && issue.Source.Name == "catalog-campaign" {
					found = true
				}
			}
			if !found {
				t.Fatal("incompatible profile lost its preparation diagnostic")
			}
			assertUnchangedPreparationKeepsOccupancy(t, fixture)
			restore()
			if err := fixture.service.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			healthy := fixture.prepared(t)
			if healthy.Revision != partial.Revision || healthy.ConfigVersion != partial.ConfigVersion {
				t.Fatal("recovery changed the measurement or configuration identity")
			}
			if len(healthy.Accepted.DispatchOccupancy) != 1 ||
				healthy.Accepted.DispatchOccupancy[0].Provenance == nil ||
				healthy.Accepted.DispatchOccupancy[0].Provenance.Source == measuredOccupancySource {
				t.Fatal("changed preparation outcome did not publish a fresh occupancy selection")
			}
		})
	}
}

func assertUnchangedPreparationKeepsOccupancy(t *testing.T, fixture *preparationCatalogFixture) {
	t.Helper()
	ctx := context.Background()
	// A permanently incompatible preparation still has measured occupancy.
	// Refresh the real cache so both selection and scan publication matter.
	input := fixture.service.occupancy.published.Load().input
	now := time.Now().UTC()
	assertMeasured := func(want time.Duration) {
		t.Helper()
		read, err := fixture.service.ReadCatalog(ctx)
		if err != nil || read.Accepted == nil || len(read.Accepted.DispatchOccupancy) != 1 {
			t.Fatalf("measured occupancy unavailable: %+v err=%v", read, err)
		}
		got := read.Accepted.DispatchOccupancy[0]
		if got.ProfileID != fixture.profile.ID || got.Batch != 1 || got.PerRequest != want ||
			got.Provenance == nil || got.Provenance.Source != measuredOccupancySource || got.Provenance.Value != int64(want) {
			t.Errorf("accepted catalog lost measured occupancy: %+v want=%s", got, want)
		}
	}
	for scan, value := range []time.Duration{198305, 287654} {
		if err := fixture.service.occupancy.refresh(ctx, input, now,
			func(context.Context, *rollingOccupancyInput, time.Time, time.Time) ([]serving.OccupancyEstimate, error) {
				if scan == 1 {
					// Refresh completes while aggregation is in flight, before
					// its result is available for cache publication.
					if err := fixture.service.Refresh(ctx); err != nil {
						return nil, err
					}
					assertMeasured(198305)
				}
				return []serving.OccupancyEstimate{rollingEstimate(input, fixture.profile.ID, 1,
					int(input.configuration.DispatchOccupancy.MinimumPairs), float64(value), now)}, nil
			}); err != nil {
			t.Fatal(err)
		}
		assertMeasured(value)
	}
}

type preparationCatalogFixture struct {
	service      *SchedulerService
	profile      serving.Profile
	declarations client.Client
	resources    map[string]any
	evidence     preparationEvidenceObjects
	nodeFailed   atomic.Bool
	imageFailed  atomic.Bool
}

func (f *preparationCatalogFixture) fail(boundary string, failed bool) {
	if boundary == "node" {
		f.nodeFailed.Store(failed)
	} else {
		f.imageFailed.Store(failed)
	}
}

func (f *preparationCatalogFixture) prepared(t *testing.T) servingkube.CatalogRead {
	t.Helper()
	read, err := f.service.ReadCatalog(context.Background())
	if err != nil || read.Accepted == nil || len(read.Accepted.Profiles) != 1 || len(read.Accepted.Preparations) != 1 {
		t.Fatalf("accepted preparation unavailable: %+v err=%v", read, err)
	}
	spec, ok := read.Accepted.Preparations[f.profile.ID]
	if !ok || spec.Instance.Node != (serving.NodeReference{Name: f.profile.NodeName, UID: f.profile.Condition.NodeUID}) ||
		spec.Config.RuntimeImage != "registry/runtime:current" || spec.Instance.RuntimeImage != spec.Config.RuntimeImage ||
		spec.MemoryBytes != uint64(f.profile.Condition.Actor.MemoryLimitBytes) || spec.Bundle.Size == 0 {
		t.Fatalf("accepted preparation lost current declaration or measured identity/envelope: %+v", spec)
	}
	if read.Accepted.Profiles[0].Condition.Actor.RuntimeImage != f.profile.Condition.Actor.RuntimeImage {
		t.Fatal("serving declaration rewrote measured runtime provenance")
	}
	for _, issue := range read.Accepted.Insufficiencies {
		if issue.Code == preparationUnavailable {
			t.Fatalf("recovered preparation retained a failure diagnostic: %+v", issue)
		}
	}
	return read
}

func newPreparationCatalogFixture(t *testing.T) *preparationCatalogFixture {
	t.Helper()
	config := schedulerTestConfiguration()
	config.Settings.ServingMemoryBytes = 128 << 20
	policy := syntheticOccupancyPolicy()
	config.DispatchOccupancy = &policy
	service := schedulerTestService(t, config)
	resources, objects := preparationCatalogEvidence(t)
	var sourceFailed atomic.Bool
	discovery := newMeasurementCatalogSourceServer(t, &sourceFailed)
	t.Cleanup(discovery.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := resources[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Error(err)
			}
			return
		}
		discovery.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	store, err := servingkube.NewStore(&rest.Config{Host: server.URL}, service.store.Client().Scheme())
	if err != nil {
		t.Fatal(err)
	}
	service.catalog, err = servingkube.NewCatalog(store, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	service.costs, err = servingkube.NewCostEvidence(store, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	service.opts.ProfileNamespaces = []string{"serving"}
	view, err := service.catalog.Refresh(context.Background(), service.opts.ProfileNamespaces)
	if err != nil || len(view.CurrentProfiles()) != 1 || len(view.Insufficiencies) != 0 {
		t.Fatalf("retained evidence fixture did not pass catalog admission: %+v err=%v", view, err)
	}
	fixture := &preparationCatalogFixture{service: service, profile: view.CurrentProfiles()[0], resources: resources, evidence: objects}
	if _, err := service.kube.CoreV1().Nodes().Create(context.Background(), &core.Node{ObjectMeta: meta.ObjectMeta{
		Name: fixture.profile.NodeName, UID: types.UID(fixture.profile.Condition.NodeUID),
	}}, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	service.kube.(*kubefake.Clientset).PrependReactor("get", "nodes", func(kubetesting.Action) (bool, runtime.Object, error) {
		if fixture.nodeFailed.Load() {
			return true, nil, context.DeadlineExceeded
		}
		return false, nil, nil
	})
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": "chill.dacs.io/runtime-declaration.v1alpha2", "image": "registry/runtime:current",
		"architecture": "amd64", "runtime": map[string]any{"family": "firstfloor", "backends": []string{"ort-cpu"}},
		"verification": "exact-image-runtime-inspection-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := json.Marshal(map[string]any{
		"schemaVersion": "chill.dacs.io/profiler-execution-facts.v1alpha8", "metricObserverImage": "registry/runtime:current",
		"declarations": []any{map[string]any{"deviceClass": fixture.profile.Condition.DeviceClass,
			"node": fixture.profile.NodeName, "runtime": map[string]string{"payload": string(payload)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.declarations = ctrlfake.NewClientBuilder().WithScheme(service.store.Client().Scheme()).WithObjects(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "execution-facts", UID: "facts-uid", ResourceVersion: "1"},
		Data:       map[string]string{"execution-facts.yaml": string(facts)},
	}).Build()
	reader := interceptor.NewClient(fixture.declarations.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if fixture.imageFailed.Load() {
				return context.DeadlineExceeded
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	observer, err := servingkube.NewObserver(service.cluster, service.store, service.kube, service.execution, &sdk.Client{}, servingkube.ObserverConfig{
		Namespace: "serving", RuntimeImage: func(ctx context.Context, p *serving.Profile) (string, error) {
			c := p.Condition
			return profilerkube.ResolveRuntimeImage(ctx, reader, profilerkube.CandidateResolverOptions{
				Namespace: "serving", ConfigMapName: "execution-facts", ConfigMapKey: "execution-facts.yaml",
			}, c.DeviceClass, p.NodeName, c.RuntimeFamily, c.Backend)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	service.observer = observer
	bundles := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected bundle request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(bundles.Close)
	service.objects, err = stores3.New(stores3.Config{LogicalStore: "evidence", Bucket: "bucket", Endpoint: bundles.URL,
		Region: "region", AccessKeyID: "access", SecretAccessKey: "secret", TempDir: t.TempDir(), HTTPClient: bundles.Client()})
	if err != nil {
		t.Fatal(err)
	}
	service.opts.StoreName, service.opts.StoreEndpoint, service.opts.StoreBucket = "evidence", bundles.URL, "bucket"
	service.opts.StoreRegion, service.opts.StoreCredentialsSecret = "region", "credentials"
	service.opts.OperationTimeout, service.opts.MaxEvidenceBytes = time.Minute, 1<<20
	return fixture
}

type preparationEvidenceObjects map[evidencestore.ObjectLocation][]byte

func (o preparationEvidenceObjects) OpenLocation(_ context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	payload, ok := o[location]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}
func (o preparationEvidenceObjects) Open(ctx context.Context, identity evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	return o.OpenLocation(ctx, identity.Location())
}
func (preparationEvidenceObjects) List(context.Context, string) ([]evidencestore.ObjectIdentity, error) {
	return nil, nil
}

// Build the same retained plan/Run/raw contract as kube's catalog evidence tests.
// The Catalog verifies these records; no prepared map or verified profile is injected.
func preparationCatalogEvidence(t *testing.T, identity ...string) (map[string]any, preparationEvidenceObjects) {
	t.Helper()
	suffix := ""
	if len(identity) != 0 {
		suffix = identity[0]
	}
	payload, err := os.ReadFile("../../profiling/profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var active profilingrun.RunConfig
	if err := json.Unmarshal(payload, &active); err != nil {
		t.Fatal(err)
	}
	active.Candidate.Inputs.ModelSpec.Name += suffix
	active.State.Model += suffix
	active.Actor.Model += suffix
	active.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 32, DeadlinePolicy: "keep"}
	active.Measurement.DurationToleranceSeconds = .5
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, DurationToleranceSeconds: .5,
		StartupWarmupIterations: active.Measurement.StartupWarmupIterations, WarmupBatchSizes: active.Measurement.WarmupBatchSizes,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode}
	spec, err := projection.ProjectCurvePlanSpec("catalog-plan"+suffix, active, idle, derivation.CurveConversionContract{}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := derivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	objects := preparationEvidenceObjects{}
	put := func(key, schema string, value any) campaign.StoredObjectReference {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		objects[evidencestore.ObjectLocation{Store: "evidence", Key: key}] = data
		return campaign.StoredObjectReference{Store: "evidence", Key: key, SchemaVersion: schema, Size: int64(len(data))}
	}
	planBytes, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	planRef := campaign.StoredObjectReference{Store: "evidence", Key: "plan" + suffix + ".json", SchemaVersion: plan.SchemaVersion(), Size: int64(len(planBytes))}
	objects[evidencestore.ObjectLocation{Store: planRef.Store, Key: planRef.Key}] = planBytes
	measurement := &edge.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "catalog-campaign" + suffix, UID: types.UID("campaign-uid" + suffix), Generation: 1, ResourceVersion: "1"},
		Spec: edge.BatchEnergyOptimizationSpec{
			State: edge.MeasurementStateSpec{Candidate: edge.ProfilingRunCandidateSpec{ModelSpec: active.Candidate.Inputs.ModelSpec.Name,
				Preprocessor: active.State.Preprocessor.Name, DeviceClass: active.State.DeviceClass, ExecutionPath: active.Candidate.ExecutionPath, PowerMode: active.State.PowerMode}, TargetNode: active.TargetNode.Name},
			ActiveProtocol: edge.ActiveProtocolSpec{WarmupIterations: int32(active.Measurement.WarmupIterations), DurationSeconds: 30, Dataset: active.Measurement.Dataset.Name,
				MetricObservation: edge.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31}},
			IdleProtocol: edge.IdleProtocolSpec{SettlingDurationSeconds: 60, DurationSeconds: 30,
				MetricObservation: edge.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31}},
			TrialsPerBatch: 2,
		},
		Status: edge.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1, Outcome: "MinimumFound"},
	}
	base := "/apis/" + edge.GroupVersion.String()
	resources := map[string]any{
		base: meta.APIResourceList{GroupVersion: edge.GroupVersion.String(), APIResources: []meta.APIResource{
			{Name: "batchenergyoptimizations", Kind: "BatchEnergyOptimization", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "profilingruns", Kind: "ProfilingRun", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "powermodetransitioncosts", Kind: "PowerModeTransitionCost", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
		}},
		base + "/namespaces/serving/powermodetransitioncosts": edge.PowerModeTransitionCostList{TypeMeta: meta.TypeMeta{APIVersion: edge.GroupVersion.String(), Kind: "PowerModeTransitionCostList"}},
	}
	completed := make([]campaign.CompletedTrialReference, 0, 4)
	for index := range 4 {
		config, watts, mode := active, 6.0, campaign.WorkloadModeActive
		if index >= 2 {
			config, watts, mode = idle, 4, campaign.WorkloadModeIdle
		}
		name := fmt.Sprintf("trial-%d%s", index, suffix)
		raw, idleRaw, execution := preparationTrialRecords(config, name+"-pod", watts)
		var rawRef campaign.StoredObjectReference
		if mode == campaign.WorkloadModeIdle {
			rawRef = put(name+"-raw", profilerevidence.IdleRawMeasurementSchemaVersion, idleRaw)
		} else {
			rawRef = put(name+"-raw", profilerevidence.RawMeasurementSchemaVersion, raw)
		}
		execution.RawMeasurement = evidencestore.ObjectIdentity{Store: rawRef.Store, Key: rawRef.Key, Size: rawRef.Size}
		executionRef := put(name+"-execution", profilerevidence.ExecutionEvidenceSchemaVersion, execution)
		runMeasurement := edge.ProfilingRunMeasurementSpec{Batch: 1, WarmupIterations: int32(config.Measurement.WarmupIterations), DurationSeconds: 30,
			WorkloadMode: config.Measurement.WorkloadMode, StartupWarmupIterations: int32(config.Measurement.StartupWarmupIterations), Workload: config.Measurement.WorkloadName}
		for _, batch := range config.Measurement.WarmupBatchSizes {
			runMeasurement.WarmupBatchSizes = append(runMeasurement.WarmupBatchSizes, int32(batch))
		}
		if config.Measurement.Dataset != nil {
			runMeasurement.Dataset = config.Measurement.Dataset.Name
		}
		if mode == campaign.WorkloadModeIdle {
			settling := int32(config.Measurement.SettlingDurationSeconds)
			runMeasurement.SettlingDurationSeconds = &settling
		}
		run := edge.ProfilingRun{TypeMeta: meta.TypeMeta{APIVersion: edge.GroupVersion.String(), Kind: "ProfilingRun"},
			ObjectMeta: meta.ObjectMeta{Namespace: measurement.Namespace, Name: name, UID: types.UID(name + "-uid"), Generation: 1,
				OwnerReferences: []meta.OwnerReference{*meta.NewControllerRef(measurement, edge.GroupVersion.WithKind("BatchEnergyOptimization"))}},
			Spec: edge.ProfilingRunSpec{MeasurementStateSpec: measurement.Spec.State, Measurement: runMeasurement, MetricObservation: measurement.Spec.ActiveProtocol.MetricObservation},
			Status: edge.ProfilingRunStatus{ObservedGeneration: 1,
				Conditions: []meta.Condition{{Type: edge.ProfilingRunConditionExecutionComplete, Status: meta.ConditionTrue, ObservedGeneration: 1},
					{Type: edge.ProfilingRunConditionEvidenceVerified, Status: meta.ConditionTrue, ObservedGeneration: 1}},
				Job: &edge.ProfilingRunObjectReference{Name: execution.Terminal.Job.Name, UID: execution.Terminal.Job.UID},
				ObservedExecution: &edge.ProfilingRunObservedExecutionStatus{Pod: edge.ProfilingRunObjectReference{Name: execution.Terminal.Pod.Name, UID: execution.Terminal.Pod.UID},
					Node: edge.ProfilingRunObjectReference{Name: config.TargetNode.Name, UID: config.TargetNode.UID}},
				Evidence: &edge.ProfilingRunEvidenceReference{Store: executionRef.Store, Key: executionRef.Key, Size: executionRef.Size, SchemaVersion: executionRef.SchemaVersion}},
		}
		resources[base+"/namespaces/serving/profilingruns/"+name] = run
		completed = append(completed, campaign.CompletedTrialReference{Run: campaign.RunReference{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)},
			Measurement: campaign.ResultMeasurement{WorkloadMode: mode, Batch: 1}, Evidence: executionRef})
	}
	result, err := campaign.NewResultRecord(campaign.ResultRecordSpec{Outcome: "MinimumFound",
		MeasurementState: campaign.ResultMeasurementState{TargetNode: active.TargetNode.Name, ModelSpec: active.Candidate.Inputs.ModelSpec.Name,
			DeviceClass: active.State.DeviceClass, ExecutionPath: active.Candidate.ExecutionPath, PowerMode: active.State.PowerMode},
		CurvePlan: planRef, CompletedTrials: completed, Detail: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := result.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	measurement.Status.Result = &edge.ProfilingRunEvidenceReference{Store: "evidence", Key: "result" + suffix + ".json", SchemaVersion: result.SchemaVersion(), Size: int64(len(resultBytes))}
	objects[evidencestore.ObjectLocation{Store: "evidence", Key: "result" + suffix + ".json"}] = resultBytes
	resources[base+"/namespaces/serving/batchenergyoptimizations"] = edge.BatchEnergyOptimizationList{
		TypeMeta: meta.TypeMeta{APIVersion: edge.GroupVersion.String(), Kind: "BatchEnergyOptimizationList"}, Items: []edge.BatchEnergyOptimization{*measurement}}
	return resources, objects
}

func preparationTrialRecords(config profilingrun.RunConfig, podUID string, watts float64) (profilerevidence.RawMeasurement, profilerevidence.IdleRawMeasurement, profilerevidence.ExecutionEvidence) {
	start := time.Date(2026, 8, 11, 1, 2, 3, 0, time.UTC)
	end, completed := start.Add(30*time.Second), start.Add(time.Minute)
	targetDuration := float64(config.Measurement.DurationSeconds)
	cpu := profilingrun.RuntimeCPU{CgroupCPUMax: "200000 100000", ORTIntraOpThreads: 2, ORTInterOpThreads: 1, AffinityCPUCount: 2, AffinityCPUs: []int{0, 1}, CPUSetEffective: "0-3"}
	provider := "CPUExecutionProvider"
	derived := profilingrun.DerivedContract{Backend: config.State.Backend, Provider: provider, NodeSnapshot: config.TargetNode}
	pod := workloadobserver.Pod{Namespace: "serving", Name: "profiling-run-pod", UID: podUID, Phase: "Running", Images: []string{config.Images.Runtime}}
	thermal := config.Metrics.ThermalMetrics
	metrics := metricobserver.Result{Source: metricobserver.SourceIdentity{Kind: config.Metrics.SourceKind, NodeName: config.TargetNode.Name,
		Endpoint: config.Metrics.Endpoint, Metric: config.Metrics.PowerMetric, MetricLabels: config.Metrics.MetricLabels, ThermalEndpoint: config.Metrics.ThermalEndpoint,
		ThermalMetrics: &metricobserver.ThermalMetricIdentity{ObservationAvailable: thermal.ObservationAvailable, Active: thermal.Active,
			CPUCoreThrottleEvents: thermal.CPUCoreThrottleEvents, CPUPackageThrottleEvents: thermal.CPUPackageThrottleEvents, Latched: thermal.Latched,
			MaximumTemperatureCelsius: thermal.MaximumTemperatureCelsius}},
		StartedAt: start, EndedAt: end.Add(time.Second), Samples: []metricobserver.Sample{{ObservedAt: start, Watts: watts}, {ObservedAt: end, Watts: watts}},
		Failures: []metricobserver.Failure{}, Summary: metricobserver.Summary{Attempts: 2, SuccessfulSamples: 2, ObservationDurationSeconds: 31, MaximumSampleGapSeconds: 30, SourceTimestampAbsent: true}}
	workload := &workloadobserver.Result{SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target:            workloadobserver.TargetIdentity{NodeName: config.TargetNode.Name, NodeUID: config.TargetNode.UID},
		Run:               workloadobserver.RunIdentity{Namespace: pod.Namespace, JobUID: "job-" + podUID, PodName: pod.Name, PodUID: podUID},
		CoverageStartedAt: start.Add(-time.Second), CoverageEndedAt: end.Add(time.Second), InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{pod}, FinalActivePods: []workloadobserver.Pod{pod}, Continuous: true}
	raw := profilerevidence.RawMeasurement{SchemaVersion: profilerevidence.RawMeasurementSchemaVersion, RunConfig: config, PodUID: podUID, Metrics: metrics, Workload: workload,
		Runtime: profilingrun.RuntimeResult{SchemaVersion: profilingrun.RuntimeResultSchemaVersion, Status: "Succeeded", ExperimentID: podUID,
			NodeName: config.TargetNode.Name, Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
			Backend: config.State.Backend, Provider: provider, AvailableProviders: []string{provider}, Inputs: config.State.Preprocessor.Inputs,
			BatchSize: 1, WarmupIterations: config.Measurement.WarmupIterations, WarmupCompletedAt: start.Add(-time.Second).Format(time.RFC3339Nano), InferenceCount: 60,
			MeasurementMode: "duration", MeasurementStartedAt: start.Format(time.RFC3339Nano), MeasurementEndedAt: end.Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30,
			TargetDurationSeconds: &targetDuration, MemoryBytes: 4096, CPUContract: cpu, DerivedContract: derived,
			RepetitionResults: []profilingrun.Repetition{{Repetition: 1, MeasurementStartedAt: start.Format(time.RFC3339Nano), MeasurementEndedAt: end.Format(time.RFC3339Nano), LatencyMilliseconds: make([]float64, 60)}}}}
	modelFiles := []string{"/model/" + filepath.Base(config.State.ArtifactFiles[0])}
	metadata := profilingrun.FirstfloorRuntimeEvidence{Mode: "profiling", DeadlinePolicy: native.DeadlineKeep, ModelInstanceCount: 1, MaxBatchSize: 1, MaxQueueSize: 32,
		Runtime: native.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "1.16.0", ModelFiles: modelFiles, Inputs: config.State.Preprocessor.Inputs, Outputs: config.State.Preprocessor.Outputs,
			Providers: []string{provider}, AvailableProviders: []string{provider}, Device: "cpu", CPUAffinity: []int{0, 1}, IntraOpThreads: 2, InterOpThreads: 1, InitializedBatches: []int{1},
			CgroupCPUMax: "200000 100000", CPUSet: "0-3", WarmupIterations: config.Actor.WarmupIterations}}
	if config.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		active := metadata
		active.ExecutionCount, active.InferenceCount, active.BatchExecutionCounts = 60, 60, map[string]uint64{"1": 60}
		raw.Runtime.Firstfloor, raw.Runtime.MemorySource = &active, "cgroup-v2-memory.peak:runtime-container-lifetime"
		repetition := &raw.Runtime.RepetitionResults[0]
		for index := range 60 {
			repetition.LatencyMilliseconds[index] = 1
			repetition.NativeSamples = append(repetition.NativeSamples, profilingrun.NativeServiceSample{
				BatchID: uint64(index + 1), BatchSize: 1, RecordIDs: []string{fmt.Sprintf("r%d", index+1)}, Units: []int{1},
				RunnerServiceMilliseconds: .5, BackendCallMilliseconds: .25, NativeRequestMilliseconds: .75,
				ServiceSample: fixtures.ServiceSample(podUID, uint64(index), 500000)})
		}
	}
	idle := profilerevidence.IdleRawMeasurement{SchemaVersion: profilerevidence.IdleRawMeasurementSchemaVersion, RunConfig: config, PodUID: podUID, Metrics: metrics, Workload: workload,
		Runtime: profilingrun.IdleRuntimeResult{SchemaVersion: profilingrun.IdleRuntimeResultSchemaVersion, Status: "Succeeded", PodUID: podUID, NodeName: config.TargetNode.Name,
			Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0", Backend: config.State.Backend, Provider: provider,
			AvailableProviders: []string{provider}, Inputs: config.State.Preprocessor.Inputs, ModelLoadCompletedAt: start.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt: start.Add(-60 * time.Second).Format(time.RFC3339Nano), SettlingCompletedAt: start.Format(time.RFC3339Nano), MeasurementStartedAt: start.Format(time.RFC3339Nano),
			MeasurementEndedAt: end.Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true, CPUContract: cpu, DerivedContract: derived}}
	metadata.BatchExecutionCounts = map[string]uint64{}
	idle.Runtime.Firstfloor, idle.Runtime.MemoryBytes, idle.Runtime.MemorySource = &metadata, 4096, "cgroup-v2-memory.peak:runtime-container-lifetime"
	execution := profilerevidence.ExecutionEvidence{SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, RunConfig: config, FinalizedAt: completed.Add(2 * time.Second),
		Terminal: profilerevidence.TerminalFacts{Job: profilerevidence.JobTerminalFacts{Namespace: pod.Namespace, Name: "job-" + podUID, UID: "job-" + podUID, Succeeded: 1, CompletionTime: completed},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: pod.Namespace, Name: pod.Name, UID: podUID, ControllerOwnerUID: "job-" + podUID, NodeName: config.TargetNode.Name, Phase: "Succeeded"},
			Node: profilerevidence.NodeTerminalFacts{Name: config.TargetNode.Name, UID: config.TargetNode.UID}, NodeWorkload: profilerevidence.NodeWorkload{ObservedAt: completed.Add(time.Second),
				Pods: []profilerevidence.WorkloadPod{{Namespace: pod.Namespace, Name: pod.Name, UID: podUID, Phase: "Succeeded", Images: pod.Images}}}}}
	images := map[string]string{"runtime": config.Images.Runtime, "artifact": config.Images.Artifact, "preprocessor": config.State.Preprocessor.Image,
		"metric-observer": config.Images.MetricObserver, "workload-observer": config.Images.WorkloadObserver}
	if config.Measurement.Dataset != nil {
		images["dataset"] = config.Measurement.Dataset.Image
	}
	for name, image := range images {
		execution.Terminal.Containers = append(execution.Terminal.Containers, profilerevidence.ContainerTerminal{Name: name, RequestedImage: image, ImageID: "containerd://" + name,
			Reason: "Completed", StartedAt: start, FinishedAt: completed.Add(-time.Second)})
	}
	return raw, idle, execution
}
