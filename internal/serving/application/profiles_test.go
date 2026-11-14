package application

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	"github.com/lab-paper-code/chill/test/fixtures"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func measuredAdoptionProfile() serving.Profile {
	profile := serving.Profile{
		ID: "measurements/complete/uid", NodeName: "node",
		Condition: derivation.CurvePlanSpec{
			NodeUID: "node-uid", Backend: "ort-cuda", RuntimeImage: "registry/cuda@sha256:measured",
			Artifact: derivation.CurveArtifactContract{Files: []string{"/artifact/model.onnx"}},
			Preprocessor: derivation.CurvePreprocessorContract{
				Name: "pre", Inputs: []string{"image"}, Outputs: []string{"label"}, MaxNewTokens: 16,
			},
			ActiveProtocol: derivation.CurveProtocolContract{
				Dataset: &derivation.CurveDatasetContract{Name: "data", UID: "data-uid"},
			},
			Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 8},
		},
		RuntimeMetadata: &native.RuntimeMetadata{
			Runtime: "ort-cuda", ModelFiles: []string{"/model/model.onnx"},
			Inputs: []string{"image"}, Outputs: []string{"label"},
			GPUUUID: "GPU-measured", CPUAffinity: []int{3}, IntraOpThreads: 1, InterOpThreads: 1,
			WarmupIterations: 5, InitializedBatches: []int{1, 2, 4},
		},
		Batches: []serving.BatchProfile{
			{Batch: 1, MemoryBytes: 2048, ServiceSamples: []native.ServiceSample{fixtures.ServiceSample("physical-trial", 0, 2e6), fixtures.ServiceSample("physical-trial", 1, 1e6)}},
			{Batch: 2, MemoryBytes: 4096, ServiceSamples: []native.ServiceSample{fixtures.ServiceSample("physical-trial", 0, 3e6), fixtures.ServiceSample("physical-trial", 1, 3e6)}},
		},
	}
	profile.Condition.Actor = native.Config{
		Model: "mobilenet", Backend: "ort-cuda", RuntimeImage: profile.Condition.RuntimeImage,
		MemoryRequestBytes: 4096 + 128<<20, MemoryLimitBytes: 4096 + 128<<20,
		Inputs: []string{"image"}, Outputs: []string{"label"}, MaxNewTokens: 16,
		BatchSizes: []int{1, 2, 4}, MaxBatchSize: 4, MaxQueueRequests: 8,
		WarmupBatchSizes: []int{1, 2}, WarmupIterations: 5, GPUUUID: "GPU-measured",
	}
	profile.Condition.Actor.BuildRunnerCommand()
	for i := range profile.Batches {
		distribution, err := native.NewServiceDistribution(profile.Batches[i].ServiceSamples)
		if err != nil {
			panic(err)
		}
		profile.Batches[i].ServiceDistribution = distribution
	}
	return profile
}

func fixtureAdmissionNative(profile *serving.Profile) *native.State {
	return &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID,
		Policy: native.Policy{ProfileID: profile.ID, MaxBatchSize: profile.Condition.Actor.MaxBatchSize}}}
}

func TestPositiveProfileCarriesPairedServiceToNativeConsumer(t *testing.T) {
	profile := measuredAdoptionProfile()
	profile.Batches[0].EnergyPerInputJoules = 0.5
	profile.Batches[1].EnergyPerInputJoules = 1
	payload, err := json.Marshal(profile.NativeBundle())
	if err != nil {
		t.Fatal(err)
	}
	var bundle native.ProfileBundle
	if err := json.Unmarshal(payload, &bundle); err != nil {
		t.Fatal(err)
	}
	// Each batch holds one gapless first execution (dropped at hydration) and
	// one known-gap pair; p50 needs a single record, so the pair answers itself.
	for j, batch := range bundle.Batches {
		distribution, err := native.NewServiceDistribution(batch.ServiceSamples)
		if err != nil {
			t.Fatal(err)
		}
		if distribution.DroppedFirstExecutions() != 1 {
			t.Fatalf("gapless first execution not dropped across native bundle wire: %d", distribution.DroppedFirstExecutions())
		}
		gap := *batch.ServiceSamples[1].PrecedingIdleNanos
		if service, _, ok := distribution.QuantileNanos(&gap, 50); !ok || service != profile.Batches[j].ServiceSamples[1].ServiceDurationNanos {
			t.Fatalf("known-gap pair lost across native bundle wire: %d %v", service, ok)
		}
	}
}
func TestMeasuredPreparationKeepsSparseInitializationAndNoReadinessClaims(t *testing.T) {
	service := &SchedulerService{
		kube: fake.NewSimpleClientset(&core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}}),
		opts: Options{
			StoreName: "store", StoreEndpoint: "http://store", StoreBucket: "bucket", StoreRegion: "region",
			StoreCredentialsSecret: "actual-secret", RequestTimeout: time.Second, OperationTimeout: time.Hour,
			MaxEvidenceBytes: 64 << 20, MaxRequestBytes: 8 << 20,
		},
	}
	profile := measuredAdoptionProfile()
	profile.Condition.Firstfloor.DeadlinePolicy = native.DeadlineDrop
	spec, err := service.measuredPreparation(context.Background(), profile, 2)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Instance.Ready || spec.Instance.Profile != nil ||
		spec.Bundle.Size != 0 || spec.Config.Policy.Batches != nil {
		t.Fatal("selection fabricated readiness/evidence/publication")
	}
	if !reflect.DeepEqual(spec.Config.BatchSizes, []int{1, 2, 4}) || spec.Config.Policy.MaxBatchSize != 2 ||
		spec.MemoryBytes != uint64(profile.Condition.Actor.MemoryLimitBytes) || spec.Store.CredentialsSecretName != "actual-secret" {
		t.Fatalf("lost measured startup/delivery facts: %+v", spec)
	}
	if spec.Config.Policy.DeadlinePolicy != "" || profile.Condition.Firstfloor.DeadlinePolicy != native.DeadlineDrop || spec.Instance.Capacity != int(profile.Condition.Firstfloor.MaxQueueSize) {
		t.Fatal("measured deadline provenance leaked into serving policy or changed queue capacity")
	}
	if spec.Instance.Allocation.GPUUUID != "GPU-measured" ||
		!strings.Contains(strings.Join(spec.Config.RunnerCommand, " "),
			"--runtime ort-cuda --model-dir /model --preprocessor /preprocessor") {
		t.Fatal("measured GPU/runtime allocation changed")
	}
	if !reflect.DeepEqual(spec.Config.Inputs, []string{"image"}) ||
		!reflect.DeepEqual(spec.Config.Outputs, []string{"label"}) || spec.Config.MaxNewTokens != 16 {
		t.Fatalf("native raw contract differs from the frozen preprocessor: %+v", spec.Config)
	}
	// Runner warmup (SmolVLM2 79 s on rasp5) is bounded by the operation, not by one HTTP request.
	if spec.Config.RunnerTimeoutMS != int(time.Hour/time.Millisecond) {
		t.Fatalf("runner timeout %d ms, want the operation timeout", spec.Config.RunnerTimeoutMS)
	}
	profile.Batches[1].MemoryBytes = 0
	if _, err = service.measuredPreparation(context.Background(), profile, 2); err == nil {
		t.Fatal("missing measured memory accepted")
	}
	for _, allowed := range [][]int{{1}, {1, 2}, {1, 4}} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			profile := measuredAdoptionProfile()
			profile.Batches = profile.Batches[:len(allowed)]
			var want []string
			for i, k := range allowed {
				profile.Batches[i].Batch = k
				want = append(want, fmt.Sprint(k))
			}
			profile.Condition.Actor.WarmupBatchSizes = allowed
			profile.Condition.Actor.BuildRunnerCommand()
			spec, err := service.measuredPreparation(context.Background(), profile, 2)
			if err != nil {
				t.Fatal(err)
			}
			var warmed []string
			for i, arg := range spec.Config.RunnerCommand {
				if arg == "--warmup-batch" {
					warmed = append(warmed, spec.Config.RunnerCommand[i+1])
				}
			}
			if !reflect.DeepEqual(warmed, want) ||
				!reflect.DeepEqual(spec.Config.BatchSizes, []int{1, 2, 4}) {
				t.Fatalf("warmup %v, want measured %v; initialized %v", warmed, want, spec.Config.BatchSizes)
			}
		})
	}
}

func TestPreparationKeepsMeasuredTagAcrossDeclarationChanges(t *testing.T) {
	ctx := context.Background()
	service := schedulerTestService(t, schedulerTestConfiguration())
	profile := measuredAdoptionProfile()
	profile.Condition.RuntimeImage = "registry/runtime:measured-A"
	profile.Condition.Actor.RuntimeImage = profile.Condition.RuntimeImage
	profile.Condition.DeviceClass, profile.Condition.RuntimeFamily = "gpu", "firstfloor"
	profile.Condition.Actor.ArtifactImage = "registry/artifact:measured"
	profile.Condition.Actor.ArtifactFiles = profile.Condition.Artifact.Files
	profile.Condition.Actor.PreprocessorImage = "registry/preprocessor:measured"
	profile.Condition.Actor.PreprocessorPath = "/preprocessor/pre"
	profile.Condition.Artifact.Image = profile.Condition.Actor.ArtifactImage
	profile.Condition.Preprocessor.Image = profile.Condition.Actor.PreprocessorImage
	profile.Condition.Preprocessor.Path = profile.Condition.Actor.PreprocessorPath
	if _, err := service.kube.CoreV1().Nodes().Create(ctx, &core.Node{ObjectMeta: meta.ObjectMeta{Name: profile.NodeName, UID: "node-uid"}}, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	declaration := func(image string) *core.ConfigMap {
		payload, err := json.Marshal(map[string]any{
			"schemaVersion": "chill.dacs.io/runtime-declaration.v1alpha2", "image": image,
			"architecture": "amd64", "runtime": map[string]any{"family": "firstfloor", "backends": []string{"ort-cuda"}},
			"verification": "exact-image-runtime-inspection-v1",
		})
		if err != nil {
			t.Fatal(err)
		}
		facts, err := json.Marshal(map[string]any{
			"schemaVersion": "chill.dacs.io/profiler-execution-facts.v1alpha8", "metricObserverImage": image,
			"declarations": []any{map[string]any{
				"deviceClass": "gpu", "node": profile.NodeName, "runtime": map[string]string{"payload": string(payload)},
				"access": map[string]any{"gpuUUID": profile.RuntimeMetadata.GPUUUID, "resources": map[string]string{"nvidia.com/gpu": "1"}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Name: "execution-facts", Namespace: "serving", UID: "facts-uid", ResourceVersion: "1"}, Data: map[string]string{"execution-facts.yaml": string(facts)}}
	}
	const declaredImage = "registry/runtime:current-B"
	reader := ctrlfake.NewClientBuilder().WithScheme(service.store.Client().Scheme()).WithObjects(declaration(declaredImage)).Build()
	resolves := 0
	observer, err := servingkube.NewObserver(service.cluster, service.store, service.kube, service.execution, &sdk.Client{}, servingkube.ObserverConfig{
		Namespace: "serving", RuntimeImage: func(ctx context.Context, p *serving.Profile) (string, error) {
			resolves++
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
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "only immutable bundle upload expected", http.StatusBadRequest)
			return
		}
		puts.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	service.objects, err = stores3.New(stores3.Config{
		LogicalStore: "store", Bucket: "bucket", Endpoint: server.URL, Region: "region",
		AccessKeyID: "access", SecretAccessKey: "secret", TempDir: t.TempDir(), HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	service.opts = Options{
		StoreName: "store", StoreEndpoint: server.URL, StoreBucket: "bucket", StoreRegion: "region", StoreCredentialsSecret: "credentials",
		OperationTimeout: time.Minute, MaxEvidenceBytes: 1 << 20, MaxRequestBytes: 1 << 20,
	}
	config := schedulerTestConfiguration()
	config.Settings.ServingMemoryBytes = 128 << 20
	spec, err := service.preparation(ctx, config, profile)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Config.RuntimeImage != profile.Condition.RuntimeImage || spec.Instance.RuntimeImage != spec.Config.RuntimeImage || resolves != 0 || puts.Load() != 1 {
		t.Fatalf("preparation did not retain measured image: %+v resolves=%d puts=%d", spec, resolves, puts.Load())
	}
	current := &core.ConfigMap{}
	if err := reader.Get(ctx, types.NamespacedName{Namespace: "serving", Name: "execution-facts"}, current); err != nil {
		t.Fatal(err)
	}
	current.Data = declaration("registry/runtime:later-C").Data
	if err := reader.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	pod, _, err := service.operations.PreparationPod(spec)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.Containers[0].Image != profile.Condition.RuntimeImage || pod.Spec.InitContainers[2].Image != profile.Condition.RuntimeImage || resolves != 0 {
		t.Fatal("launch used declared image instead of measured reference")
	}
	if spec.Instance.Profile.Condition.Actor.RuntimeImage != profile.Condition.Actor.RuntimeImage || profile.Condition.Actor.RuntimeImage == declaredImage {
		t.Fatal("current launch image rewrote historical measurement provenance")
	}
}

// Queue bytes fit inside the frozen measured envelope, rather than defining a
// second serving-only memory limit.
func TestMeasuredPreparationBoundsQueueWithinFrozenEnvelope(t *testing.T) {
	service := &SchedulerService{
		kube: fake.NewSimpleClientset(&core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}}),
		opts: Options{
			StoreName: "store", StoreEndpoint: "http://store", StoreBucket: "bucket", StoreRegion: "region",
			StoreCredentialsSecret: "actual-secret", RequestTimeout: time.Second, OperationTimeout: time.Hour,
			MaxEvidenceBytes: 64 << 20, MaxRequestBytes: 8 << 20,
		},
	}
	spec, err := service.measuredPreparation(context.Background(), measuredAdoptionProfile(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	if spec.MemoryBytes != 4096+128<<20 || spec.Instance.Allocation.MemoryBytes != spec.MemoryBytes ||
		spec.Config.MaxQueueBytes != 64<<20 {
		t.Fatalf("limit %d, queue %d", spec.MemoryBytes, spec.Config.MaxQueueBytes)
	}
	if 2*uint64(spec.Config.MaxQueueBytes) > spec.MemoryBytes-4096 {
		t.Fatal("queued bytes exceed the memory headroom")
	}
	if _, err = service.measuredPreparation(context.Background(), measuredAdoptionProfile(), 1); err == nil {
		t.Fatal("headroom without queue bytes accepted")
	}
}
func TestMeasuredPreparationUsesSharedCPUWithoutAllocation(t *testing.T) {
	node := &core.Node{
		ObjectMeta: meta.ObjectMeta{
			Name: "node", UID: "node-uid",
		},
		Status: core.NodeStatus{
			Conditions: []core.NodeCondition{{Type: core.NodeReady, Status: core.ConditionTrue}},
			Allocatable: core.ResourceList{
				core.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
	}
	service := &SchedulerService{
		kube: fake.NewSimpleClientset(node),
		opts: Options{
			StoreName: "store", StoreEndpoint: "http://store", StoreBucket: "bucket", StoreRegion: "region",
			StoreCredentialsSecret: "actual-secret", RequestTimeout: time.Second, OperationTimeout: time.Hour,
			MaxEvidenceBytes: 64 << 20, MaxRequestBytes: 8 << 20,
		},
	}
	for _, threads := range []int{0, 2, 8} {
		t.Run(fmt.Sprint(threads), func(t *testing.T) {
			profile := measuredAdoptionProfile()
			profile.RuntimeMetadata.CPUAffinity = []int{0, 1, 2, 3}
			profile.RuntimeMetadata.IntraOpThreads, profile.RuntimeMetadata.InterOpThreads = threads, 0
			profile.RuntimeMetadata.CgroupCPUMax = "max 100000"
			profile.RuntimeMetadata.GPUUUID = ""
			profile.Condition.Backend = "ort-cpu"
			profile.Condition.Actor.Backend = profile.Condition.Backend
			profile.Condition.Actor.GPUUUID = ""
			profile.Condition.Actor.BuildRunnerCommand()
			spec, err := service.measuredPreparation(context.Background(), profile, 128<<20)
			if err != nil {
				t.Fatal(err)
			}
			command := strings.Join(spec.Config.RunnerCommand, " ")
			if strings.Contains(command, "--intra-op-threads") || strings.Contains(command, "--cpu-affinity") {
				t.Fatalf("shared serving preparation injected CPU controls: %+v", spec)
			}
			inventory := serving.SchedulerNode{
				Node: spec.Instance.Node, MemoryBytes: 1 << 30,
			}
			if err = servingkube.ValidatePreparationCapacity(
				"serving", node, &core.PodList{}, inventory, spec.Instance,
			); err != nil {
				t.Fatalf("admitted measured preparation failed capacity: %v", err)
			}
		})
	}
}

func TestMeasuredSupplementalGroupsAreReproducible(t *testing.T) {
	profile := measuredAdoptionProfile()
	profile.Condition.RuntimeAccess = &derivation.CurveRuntimeAccessContract{SupplementalGroups: []int64{44}}
	if err := reproducibleRuntimeAccess(profile); err != nil {
		t.Fatalf("measured supplemental groups refused: %v", err)
	}
}

func TestGeneratedPreparationRejectsUnrepresentableMeasuredRuntimeAccess(t *testing.T) {
	for _, access := range []derivation.CurveRuntimeAccessContract{
		{Privileged: true, SupplementalGroups: []int64{44}},
		{Resources: map[string]string{"other.device/accelerator": "1"}},
	} {
		profile := measuredAdoptionProfile()
		profile.Condition.RuntimeAccess = &access
		// No clients: reject before any external lookup or configuration write.
		spec, err := (&SchedulerService{}).measuredPreparation(context.Background(), profile, 0)
		if err == nil || !strings.Contains(err.Error(), "cannot reproduce measured runtimeAccess") {
			t.Fatalf("measured delivery access silently dropped: spec=%+v err=%v", spec, err)
		}
		if spec.Name != "" {
			t.Fatal("unrepresentable delivery produced a preparation")
		}
	}
}

func TestFailedRefreshPreservesAcceptedProfilesAndRouting(t *testing.T) {
	const acceptedVersion = "accepted-config"
	ctx := context.Background()
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	execution, err := serving.NewRuntime(ctx, serving.RuntimeConfig{ObservationLog: observationLog})
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
	store, err := servingkube.NewStore(&rest.Config{Host: "http://127.0.0.1:1"}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	settings := withSchedulerEstimateFixture(serving.SchedulerSettings{
		ObservationWindow: time.Second,
		ControlInterval:   time.Second, ComparisonPeriod: time.Second,
		MaxReplicas:  1,
		MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
		CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
		PlanSearchStrategy: "v1", PlanSearchEvaluations: 500000, PlanExactFinalists: 16, PlanPolishActive: 4,
		CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
	})
	// The native store is unreachable, so listing completed measurements fails.
	rejected := SchedulerConfiguration{Settings: settings}
	raw, err := json.Marshal(rejected)
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "new"},
		Data:       map[string]string{"scheduler.json": string(raw)},
	})
	cluster := servingkube.NewClusterState(store, kube, "serving")
	observer, err := servingkube.NewObserver(cluster, store, kube, execution, &sdk.Client{}, servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	objects := &stores3.Store{}
	catalog, err := servingkube.NewCatalog(store, objects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	profile := measuredAdoptionProfile()
	profile.Condition.LogicalModel = "model"
	observer.SetProfiles([]serving.Profile{profile})
	definition := &serving.Definition{Admission: serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024},
		Name: "workload", UID: "workload-uid", Generation: 1,
		SLO:     serving.SLO{Percentile: 99, TargetLatency: time.Second},
		Dataset: &serving.DatasetReference{Name: "data", UID: "data-uid"},
		Preprocessor: serving.Preprocessor{
			Name: "pre", Input: []serving.RawField{{Name: "image"}}, Output: []serving.RawField{{Name: "label"}},
			MaxNewTokens: 16,
		},
		Model: serving.Model{Name: "model", UID: "model-uid", Generation: 1, Paths: []serving.ModelPath{{
			Files: profile.Condition.Artifact.Files, Backend: profile.Condition.Backend,
			Firstfloor: profile.Condition.Firstfloor,
		}}},
	}
	if err := execution.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err := execution.InstallInstances([]serving.ReadyInstance{{
		State: serving.InstanceState{
			ID: "physical", Node: serving.NodeReference{Name: "node", UID: "uid"},
			Profile: profile.ID, Ready: true, ObservedAt: time.Now(), Native: fixtureAdmissionNative(&profile),
		},
		Profile: &profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
		Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
			t.Error("refresh must not dispatch inference")
			return serving.BackendResult{}, nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	execution.EnableSchedulerRouting()
	before, err := execution.State(definition.Name)
	if err != nil || !before.Admission || before.Reason != "Ready" {
		t.Fatalf("baseline route: %+v %v", before, err)
	}
	accepted := &SchedulerConfiguration{
		Settings: settings, StaticRouting: map[string]map[string]int{definition.UID: {"physical": 1}},
	}
	service := &SchedulerService{
		cluster: cluster,
		kube:    kube, observer: observer, objects: objects, catalog: catalog, resourceVersion: acceptedVersion,
		sources: profile.ID,
		opts: Options{ServingNamespace: "serving", SchedulerConfigMap: "scheduler",
			SchedulerConfigKey: "scheduler.json", MaxRequestBytes: 1 << 20, MaxEvidenceBytes: 1024},
	}
	service.configuration.Store(accepted)
	if err := service.Refresh(ctx); err == nil {
		t.Fatal("refresh succeeded without listing completed measurements")
	}
	after, err := execution.State(definition.Name)
	if err != nil || !after.Admission || after.Reason != before.Reason {
		t.Fatalf("failed refresh changed actual runtime admission/routing: before=%+v after=%+v err=%v",
			before, after, err)
	}
	if !reflect.DeepEqual(observer.Profiles(), []serving.Profile{profile}) ||
		service.configuration.Load() != accepted ||
		service.resourceVersion != acceptedVersion {
		t.Fatal("failed refresh published partial profile/configuration state")
	}
}

func TestSchedulerStartupOnPreservesRestoredReadyWorkloadRouting(t *testing.T) {
	ctx := context.Background()
	config := schedulerTestConfiguration()
	config.Settings.AutonomousWorkloads = []string{"original-workload-uid"}
	config.Settings.Controls = serving.SchedulerControls{
		Placement: true, Resources: true, Replicas: true, Routing: true, Batching: true, Power: true, Membership: true,
	}
	service := schedulerTestService(t, config)
	profile := measuredAdoptionProfile()
	profile.Condition.LogicalModel = "model"
	service.observer.SetProfiles([]serving.Profile{profile})
	definition := &serving.Definition{Admission: serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024},
		Name: "restored", UID: "original-workload-uid", Generation: 1,
		SLO:     serving.SLO{Percentile: 99, TargetLatency: time.Second},
		Dataset: &serving.DatasetReference{Name: "data", UID: "data-uid"},
		Preprocessor: serving.Preprocessor{
			Name: "pre", Input: []serving.RawField{{Name: "image", MaxBytes: 16}}, Output: []serving.RawField{{Name: "label"}},
			MaxNewTokens: 16,
		},
		Model: serving.Model{Name: "model", UID: "original-model-uid", Generation: 1, Paths: []serving.ModelPath{{
			Files: profile.Condition.Artifact.Files, Backend: profile.Condition.Backend,
			Firstfloor: profile.Condition.Firstfloor,
		}}},
	}
	// These are the actual runtime inputs restored by Observer and Manager.
	if err := service.execution.Publish(definition); err != nil {
		t.Fatal(err)
	}
	dispatched := make(chan serving.Dispatch, 1)
	instance := serving.ReadyInstance{
		State: serving.InstanceState{ID: "original-pod/container",
			Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Profile: profile.ID, Ready: true, ObservedAt: time.Now(), Native: fixtureAdmissionNative(&profile)},
		Profile: &profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
		Execute: func(_ context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
			dispatched <- dispatch
			return serving.BackendResult{StatusCode: http.StatusOK, Finished: true}, nil
		},
	}
	if err := service.execution.InstallInstances([]serving.ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	service.execution.EnableSchedulerRouting()
	// A later observation rebuild must retain the restored baseline, not an
	// empty constructor-time freeze, and must not admit newly discovered routes.
	extra := instance
	extra.State.ID = "new-pod/container"
	if err := service.execution.InstallInstances([]serving.ReadyInstance{instance, extra}); err != nil {
		t.Fatal(err)
	}
	actual := service.execution.SchedulerInputs(time.Now).Routing[definition.UID]
	if len(actual) != 1 || actual[instance.State.ID] != 1 {
		t.Fatalf("startup actual distribution did not retain the physical baseline: %+v", actual)
	}
	receipt, err := service.execution.Receive(definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := service.execution.Accept(&receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{
		Name: "image", Datatype: "BYTES", Data: []byte("1"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := call.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if dispatch := <-dispatched; dispatch.Assignment.Instance != instance.State.ID {
		t.Fatalf("restored inference used another physical route: %+v", dispatch.Assignment)
	}
	if err := service.execution.Response(receipt, "completed", ""); err != nil {
		t.Fatal(err)
	}
}

func schedulerTestConfiguration() SchedulerConfiguration {
	return SchedulerConfiguration{
		Settings: withSchedulerEstimateFixture(serving.SchedulerSettings{
			ObservationWindow: time.Second,
			ControlInterval:   time.Second, ComparisonPeriod: time.Second,
			MaxReplicas:  1,
			MaxInstances: 5, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
			CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
			PlanSearchStrategy: "v1", PlanSearchEvaluations: 500000, PlanExactFinalists: 16, PlanPolishActive: 4,
			CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
		}),
		Reserved: map[string]serving.SchedulerAllocation{"rpi": {MemoryBytes: 1024}},
	}
}

func schedulerTestService(t *testing.T, config SchedulerConfiguration) *SchedulerService {
	t.Helper()
	ctx := context.Background()
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	execution, err := serving.NewRuntime(ctx, serving.RuntimeConfig{ObservationLog: observationLog})
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
	observer, err := servingkube.NewObserver(cluster, store, kube, execution, &sdk.Client{}, servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	operations, err := servingops.NewOperations(cluster, store, kube, execution, observer, &sdk.Client{}, servingops.OperationConfig{Namespace: "serving", PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	objects := &stores3.Store{}
	catalog, err := servingkube.NewCatalog(store, objects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := servingkube.NewSchedulerController(cluster, store, kube, execution, observer, operations, observationLog, servingkube.SchedulerControllerConfig{Settings: config.Settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	service := &SchedulerService{
		cluster: cluster,
		kube:    kube, store: store, execution: execution, observer: observer, objects: objects,
		catalog: catalog, operations: operations, Controller: controller, observationLog: observationLog,
		resourceVersion: "1",
		opts: Options{ServingNamespace: "serving", SchedulerConfigMap: "scheduler", SchedulerConfigKey: "scheduler.json",
			ProfileNamespaces: []string{"measurements"}, MaxRequestBytes: 1 << 20, MaxEvidenceBytes: 1024},
	}
	service.configuration.Store(&config)
	return service
}
