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
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	projection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
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

func TestRefreshPreparesRecordedRuntimeTag(t *testing.T) {
	fixture := newPreparationCatalogFixture(t)
	if err := fixture.service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.prepared(t)
	spec := fixture.service.Profiles.Load().Preparations[fixture.profile.ID]
	spec.Instance.Profile = &fixture.profile
	pod, _, err := fixture.service.operations.PreparationPod(spec)
	if err != nil {
		t.Fatal(err)
	}
	const measured = "registry/runtime:measured"
	if fixture.profile.Condition.RuntimeImage != measured ||
		pod.Spec.Containers[0].Image != measured || pod.Spec.InitContainers[2].Image != measured {
		t.Fatalf("retained tag did not reach serving containers: profile=%s pod=%+v", fixture.profile.Condition.RuntimeImage, pod.Spec)
	}
}

// A refresh builds each registered service's quantile tables for the profiles
// that can serve it, so a control round's first query finds them built.
func TestRefreshPreparesServiceQuantilesOffDecisionPath(t *testing.T) {
	fixture := newPreparationCatalogFixture(t)
	c := fixture.profile.Condition
	fields := func(names []string) []serving.RawField {
		out := make([]serving.RawField, len(names))
		for i, name := range names {
			out[i].Name = name
		}
		return out
	}
	d := &serving.Definition{
		Name: "service", UID: "service-uid", Generation: 1,
		Admission: serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024},
		SLO:       serving.SLO{Percentile: 95, TargetLatency: time.Second},
		Model: serving.Model{Name: c.LogicalModel, UID: "model-uid", Generation: 1, Paths: []serving.ModelPath{{
			Name: c.Artifact.ExecutionPath, Artifact: c.Artifact.Name, Files: c.Artifact.Files, Image: c.Artifact.Image,
			RuntimeFamily: c.RuntimeFamily, Backend: c.Backend, Firstfloor: c.Firstfloor,
			Preprocessor: serving.PreprocessorArtifact{Image: c.Preprocessor.Image, Architecture: c.Preprocessor.Architecture, Path: c.Preprocessor.Path},
		}}},
		Preprocessor: serving.Preprocessor{Name: c.Preprocessor.Name, Input: fields(c.Preprocessor.Inputs), Output: fields(c.Preprocessor.Outputs), MaxNewTokens: int32(c.Preprocessor.MaxNewTokens)},
	}
	if err := fixture.service.execution.Publish(d); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	profiles := fixture.service.Profiles.Load().Profiles
	if len(profiles) != 1 || len(profiles[0].Batches) == 0 {
		t.Fatalf("fixture profile missing: %+v", profiles)
	}
	// No query has run: a built table can only come from the refresh.
	for _, b := range profiles[0].Batches {
		if !b.ServiceDistribution.Prepared(95) {
			t.Fatalf("batch %d p95 table is left to the first decision query", b.Batch)
		}
	}
}

// Before the fix, the failed pass accepted an empty preparation map and froze
// its source signature. The healthy pass then skipped materialization forever.
func TestRefreshRecoversPreparationsAfterTransientMaterialization(t *testing.T) {
	fixture := newPreparationCatalogFixture(t)
	fixture.fail("node", true)
	if err := fixture.service.Refresh(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired materialization was not a refresh failure: %v", err)
	}
	failed, err := fixture.service.ReadCatalog(context.Background())
	if err != nil || failed.Accepted != nil || len(failed.Profiles) != 1 {
		t.Fatalf("failed materialization accepted a partial catalog or lost verified reporting: %+v err=%v", failed, err)
	}
	fixture.fail("node", false)
	if err := fixture.service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	healthy := fixture.prepared(t)
	if healthy.Revision != failed.Revision || healthy.ConfigVersion != failed.ConfigVersion {
		t.Fatal("recovery changed the measurement or configuration identity")
	}
}

func TestRefreshPreparationFailurePreservesAcceptedCatalog(t *testing.T) {
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
	fixture.fail("node", true)
	if err := fixture.service.Refresh(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("failed required read was accepted: %v", err)
	}
	after, err := fixture.service.ReadCatalog(ctx)
	if err != nil || !reflect.DeepEqual(after.Accepted, before.Accepted) ||
		fixture.service.Profiles.Load() != accepted || fixture.service.configuration.Load() != configuration {
		t.Fatalf("global preparation failure replaced accepted state: before=%+v after=%+v err=%v", before.Accepted, after.Accepted, err)
	}
	fixture.fail("node", false)
	if err := fixture.service.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	recovered := fixture.prepared(t)
	if recovered.Accepted.ConfigVersion != cm.ResourceVersion || recovered.Revision != before.Revision {
		t.Fatal("healthy refresh did not accept the pending revision with unchanged measurements")
	}
}

func TestRefreshRecoversPreparationsAfterCompatibilityRestored(t *testing.T) {
	for _, guard := range []string{"nodeAbsent", "nodeUID"} {
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
			} else {
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
			fresh, ok := batchOccupancy(healthy.Accepted.DispatchOccupancy, 1)
			if !ok || fresh.Provenance == nil || fresh.Provenance.Source == measuredOccupancySource {
				t.Fatal("changed preparation outcome did not publish a fresh occupancy selection")
			}
		})
	}
}

func TestRefreshPreparationDoesNotReadCurrentDeclaration(t *testing.T) {
	fixture := newPreparationCatalogFixture(t)
	fixture.fail("declaration", true)
	if err := fixture.service.Refresh(context.Background()); err != nil {
		t.Fatalf("current declaration failure blocked measured preparation: %v", err)
	}
	fixture.prepared(t)
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
		if err != nil || read.Accepted == nil || len(read.Accepted.DispatchOccupancy) != len(fixture.profile.Batches) {
			t.Fatalf("measured occupancy unavailable: %+v err=%v", read, err)
		}
		got, _ := batchOccupancy(read.Accepted.DispatchOccupancy, 1)
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

// batchOccupancy is the selected occupancy of one batch size.
func batchOccupancy(selected []serving.DispatchOccupancy, batch int) (serving.DispatchOccupancy, bool) {
	for _, value := range selected {
		if value.Batch == batch {
			return value, true
		}
	}
	return serving.DispatchOccupancy{}, false
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
		spec.Config.RuntimeImage != f.profile.Condition.RuntimeImage || spec.Instance.RuntimeImage != spec.Config.RuntimeImage ||
		spec.MemoryBytes != uint64(f.profile.Condition.Actor.MemoryLimitBytes) || spec.Bundle.Size == 0 {
		t.Fatalf("accepted preparation lost measured image/identity/envelope: %+v", spec)
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
	return newSessionCatalogFixture(t, nil)
}

func newSessionCatalogFixture(t *testing.T, mutate func(*derivation.SessionResult)) *preparationCatalogFixture {
	t.Helper()
	config := schedulerTestConfiguration()
	config.Settings.ServingMemoryBytes = 128 << 20
	policy := syntheticOccupancyPolicy()
	config.DispatchOccupancy = &policy
	service := schedulerTestService(t, config)
	resources, objects := preparationSessionEvidence(t, "", mutate)
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

// preparationCatalogEvidence stores one Complete measurement the way the
// session runner leaves it: a session result at the BEO's result key and the
// status that references it. The Catalog derives the profile from it alone.
func preparationCatalogEvidence(t *testing.T, identity ...string) (map[string]any, preparationEvidenceObjects) {
	t.Helper()
	suffix := ""
	if len(identity) != 0 {
		suffix = identity[0]
	}
	return preparationSessionEvidence(t, suffix, nil)
}

// preparationSessionEvidence is preparationCatalogEvidence with the session
// result edited by mutate before it is stored.
func preparationSessionEvidence(t *testing.T, suffix string, mutate func(*derivation.SessionResult)) (map[string]any, preparationEvidenceObjects) {
	t.Helper()
	payload, err := os.ReadFile("../../profiling/profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var active profilingrun.RunConfig
	if err := json.Unmarshal(payload, &active); err != nil {
		t.Fatal(err)
	}
	active.Images.Runtime = "registry/runtime:measured"
	active.Actor.RuntimeImage = active.Images.Runtime
	active.Candidate.Inputs.ModelSpec.Name += suffix
	active.State.Model += suffix
	active.Actor.Model += suffix
	active.Measurement.DurationToleranceSeconds = .5
	idle := active
	idle.LoadOriginNode, idle.LoadOriginGatewayPod = "", nil
	idle.Metrics.DurationSeconds = 31
	idle.Measurement = profilingrun.MeasurementContract{Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, DurationToleranceSeconds: .5,
		StartupWarmupIterations: active.Measurement.StartupWarmupIterations, WarmupBatchSizes: active.Measurement.WarmupBatchSizes,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode}
	// The measured contracts (artifact, preprocessor, actor) come from the
	// captured run config's projection.
	spec, err := projection.ProjectCurvePlanSpec("catalog-plan"+suffix, active, idle, derivation.CurveConversionContract{}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	actor := spec.Actor
	actor.BatchSizes, actor.MaxBatchSize, actor.WarmupBatchSizes = []int{1, 2}, 2, []int{1, 2}
	result := preparationSessionResult(t, derivation.SessionCondition{
		NodeName: active.TargetNode.Name, NodeUID: spec.NodeUID, DeviceClass: spec.DeviceClass, PowerMode: spec.PowerMode,
		LogicalModel: spec.LogicalModel, RuntimeFamily: spec.RuntimeFamily, Backend: spec.Backend, RuntimeImage: spec.RuntimeImage,
		Artifact: spec.Artifact, Preprocessor: spec.Preprocessor, Dataset: spec.ActiveProtocol.Dataset,
		Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 2, MaxQueueSize: 32, DeadlinePolicy: "keep"}, Actor: actor,
	}, metricobserver.SourceIdentity{Kind: active.Metrics.SourceKind, NodeName: active.TargetNode.Name, Endpoint: active.Metrics.Endpoint, Metric: active.Metrics.PowerMetric})
	if mutate != nil {
		mutate(&result)
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	measurement := &edge.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "catalog-campaign" + suffix, UID: types.UID("campaign-uid" + suffix), Generation: 1, ResourceVersion: "1"},
		Spec: edge.BatchEnergyOptimizationSpec{State: edge.MeasurementStateSpec{TargetNode: active.TargetNode.Name,
			Candidate: edge.ProfilingRunCandidateSpec{ModelSpec: active.Candidate.Inputs.ModelSpec.Name, Preprocessor: active.State.Preprocessor.Name,
				DeviceClass: active.State.DeviceClass, ExecutionPath: active.Candidate.ExecutionPath, PowerMode: active.State.PowerMode}}},
	}
	// The session runner's terminal status (setSessionStatus): phase Complete,
	// observed generation, MeasurementComplete and the stored result reference.
	key := servingkube.SessionResultPrefix + string(measurement.UID) + "/result.json"
	measurement.Status = edge.BatchEnergyOptimizationStatus{Phase: string(edge.MeasurementPhaseComplete), ObservedGeneration: 1,
		Outcome: "AllBatchesMeasured", CompletedBatches: int32(len(result.Batches)),
		Result: &edge.ProfilingRunEvidenceReference{Store: "evidence", Key: key, SchemaVersion: derivation.SessionResultSchemaVersion, Size: int64(len(resultBytes))},
		Conditions: []meta.Condition{{Type: "MeasurementComplete", Status: meta.ConditionTrue, Reason: "AllBatchesMeasured",
			Message: "measured batch sizes 1, 2", ObservedGeneration: 1, LastTransitionTime: meta.NewTime(time.Date(2026, 8, 11, 1, 3, 0, 0, time.UTC))}}}
	objects := preparationEvidenceObjects{{Store: "evidence", Key: key}: resultBytes}
	base := "/apis/" + edge.GroupVersion.String()
	resources := map[string]any{
		base: meta.APIResourceList{GroupVersion: edge.GroupVersion.String(), APIResources: []meta.APIResource{
			{Name: "batchenergyoptimizations", Kind: "BatchEnergyOptimization", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
			{Name: "powermodetransitioncosts", Kind: "PowerModeTransitionCost", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
		}},
		base + "/namespaces/serving/powermodetransitioncosts": edge.PowerModeTransitionCostList{TypeMeta: meta.TypeMeta{APIVersion: edge.GroupVersion.String(), Kind: "PowerModeTransitionCostList"}},
		base + "/namespaces/serving/batchenergyoptimizations": edge.BatchEnergyOptimizationList{
			TypeMeta: meta.TypeMeta{APIVersion: edge.GroupVersion.String(), Kind: "BatchEnergyOptimizationList"}, Items: []edge.BatchEnergyOptimization{*measurement}},
	}
	return resources, objects
}

// preparationSessionResult is a small valid session result for the condition's
// actor batch sizes: 60 service records per batch, each after a known idle
// gap; identified power and input slopes; the executor's runtime report; and
// the meter the power came from.
func preparationSessionResult(t *testing.T, condition derivation.SessionCondition, meter metricobserver.SourceIdentity) derivation.SessionResult {
	t.Helper()
	meterSource, err := json.Marshal(meter)
	if err != nil {
		t.Fatal(err)
	}
	result := derivation.SessionResult{
		SchemaVersion: derivation.SessionResultSchemaVersion, Condition: condition,
		Power: derivation.SessionPower{IdleWatts: *applicationTestSlope(`{"status":"Identified","value":4}`),
			AwakeStepWatts: *applicationTestSlope(`{"status":"Identified","value":1.5}`), HoldSeconds: *applicationTestSlope(`{"status":"Identified","value":0.5}`)},
		RuntimeMetadata: &native.RuntimeMetadata{Runtime: condition.Backend, RuntimeVersion: "1.16.0", Device: "cpu",
			ModelFiles: []string{"/model/" + filepath.Base(condition.Artifact.Files[0])}, Inputs: condition.Preprocessor.Inputs, Outputs: condition.Preprocessor.Outputs,
			Providers: []string{"CPUExecutionProvider"}, AvailableProviders: []string{"CPUExecutionProvider"}, IntraOpThreads: 2, InterOpThreads: 1,
			InitializedBatches: condition.Actor.BatchSizes, WarmupIterations: condition.Actor.WarmupIterations},
		MeterSource: meterSource,
	}
	for _, b := range condition.Actor.BatchSizes {
		batch := derivation.SessionBatch{Batch: b, PeakMemoryBytes: 4096, MemoryKind: "peak",
			JoulesPerInput: *applicationTestSlope(fmt.Sprintf(`{"status":"Identified","value":%g}`, 0.4/float64(b)))}
		for i := range 60 {
			batch.ServiceNanos = append(batch.ServiceNanos, int64(b)*int64(400_000+i%5*50_000))
			batch.PrecedingIdleNanos = append(batch.PrecedingIdleNanos, int64(1_000_000+i%2*99_000_000))
		}
		// No backlogged pair: occupancy stays the rolling estimate's or the
		// settings default, which the refresh and policy tests pin.
		batch.DispatchOccupancyNanos = []float64{}
		rate := float64(1000 * b)
		batch.SaturationInputRate = &rate
		result.Batches = append(result.Batches, batch)
	}
	return result
}
