package main

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
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func measuredAdoptionProfile() serving.Profile {
	return serving.Profile{
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
			CPU:        derivation.CurveCPUContract{Request: "1", Limit: "1", ORTIntraOpThreads: 1},
		},
		RuntimeMetadata: &native.RuntimeMetadata{
			Runtime: "ort-cuda", ModelFiles: []string{"/model/model.onnx"},
			Inputs: []string{"image"}, Outputs: []string{"label"},
			GPUUUID: "GPU-measured", CPUAffinity: []int{3}, IntraOpThreads: 1, InterOpThreads: 1,
			WarmupIterations: 5, InitializedBatches: []int{1, 2, 4},
		},
		Batches: []serving.BatchProfile{
			{Batch: 1, MemoryBytes: 2048, DurationNanos: []float64{2e6, 1e6}},
			{Batch: 2, MemoryBytes: 4096, DurationNanos: []float64{3e6}},
		},
	}
}
func TestMeasuredPreparationKeepsSparseInitializationAndNoReadinessClaims(t *testing.T) {
	service := &schedulerService{
		kube: fake.NewSimpleClientset(&core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}}),
		opts: options{
			storeName: "store", storeEndpoint: "http://store", storeBucket: "bucket", storeRegion: "region",
			storeCredentialsSecret: "actual-secret", requestTimeout: time.Second,
			maxEvidenceBytes: 64 << 20, maxRequestBytes: 8 << 20,
		},
	}
	profile := measuredAdoptionProfile()
	spec, err := service.measuredPreparation(context.Background(), profile, 2)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Instance.Ready || spec.Instance.Profile != nil ||
		spec.Bundle.Size != 0 || spec.Config.Policy.Batches != nil {
		t.Fatal("selection fabricated readiness/evidence/publication")
	}
	if !reflect.DeepEqual(spec.Config.BatchSizes, []int{1, 2, 4}) || spec.Config.Policy.MaxBatchSize != 2 ||
		spec.MemoryBytes != 4098 || spec.Store.CredentialsSecretName != "actual-secret" {
		t.Fatalf("lost measured startup/delivery facts: %+v", spec)
	}
	if !reflect.DeepEqual(spec.Instance.Allocation.CPUs, []int{3}) ||
		spec.Instance.Allocation.GPUUUID != "GPU-measured" ||
		!strings.Contains(strings.Join(spec.Config.RunnerCommand, " "),
			"--runtime ort-cuda --model-dir /model --preprocessor /preprocessor") {
		t.Fatal("measured GPU/runtime allocation changed")
	}
	if !reflect.DeepEqual(spec.Config.Inputs, []string{"image"}) ||
		!reflect.DeepEqual(spec.Config.Outputs, []string{"label"}) || spec.Config.MaxNewTokens != 16 {
		t.Fatalf("native raw contract differs from the frozen preprocessor: %+v", spec.Config)
	}
	bundle := profile.NativeBundle()
	if !reflect.DeepEqual(bundle.Batches[0].ServiceSamplesMS, []float64{1, 2}) ||
		profile.Batches[0].DurationNanos[0] != 2e6 {
		t.Fatal("shared bundle producer changed raw distribution")
	}
	profile.Batches[1].MemoryBytes = 0
	if _, err = service.measuredPreparation(context.Background(), profile, 2); err == nil {
		t.Fatal("missing measured memory accepted")
	}
}

// The queued request bytes firstfloor admits fit in the Pod memory above the
// measured peak: the limit adds servingMemoryBytes, half of which is the queue.
func TestMeasuredPreparationReservesQueueMemoryAboveMeasuredPeak(t *testing.T) {
	service := &schedulerService{
		kube: fake.NewSimpleClientset(&core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}}),
		opts: options{
			storeName: "store", storeEndpoint: "http://store", storeBucket: "bucket", storeRegion: "region",
			storeCredentialsSecret: "actual-secret", requestTimeout: time.Second,
			maxEvidenceBytes: 64 << 20, maxRequestBytes: 8 << 20,
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
func TestMeasuredPreparationKeepsQuotaAffinityAndThreadsDistinct(t *testing.T) {
	node := &core.Node{
		ObjectMeta: meta.ObjectMeta{
			Name: "node", UID: "node-uid",
		},
		Status: core.NodeStatus{
			Conditions: []core.NodeCondition{{Type: core.NodeReady, Status: core.ConditionTrue}},
			Allocatable: core.ResourceList{
				core.ResourceCPU: resource.MustParse("4"), core.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
	}
	service := &schedulerService{
		kube: fake.NewSimpleClientset(node),
		opts: options{
			storeName: "store", storeEndpoint: "http://store", storeBucket: "bucket", storeRegion: "region",
			storeCredentialsSecret: "actual-secret", requestTimeout: time.Second,
			maxEvidenceBytes: 64 << 20, maxRequestBytes: 8 << 20,
		},
	}
	for _, request := range []string{"500m", "1500m", "2"} {
		t.Run(request, func(t *testing.T) {
			profile := measuredAdoptionProfile()
			profile.Condition.CPU.Request, profile.Condition.CPU.Limit = request, "2"
			profile.Condition.CPU.ORTIntraOpThreads = 2
			profile.Condition.CPU.AffinityCPUs = []int{0, 1, 2, 3}
			profile.RuntimeMetadata.CPUAffinity = []int{0, 1, 2, 3}
			profile.RuntimeMetadata.IntraOpThreads = 2
			profile.RuntimeMetadata.CgroupCPUMax = "200000 100000"
			profile.RuntimeMetadata.GPUUUID = ""
			spec, err := service.measuredPreparation(context.Background(), profile, 128<<20)
			if err != nil {
				t.Fatal(err)
			}
			quantity := resource.MustParse(request)
			if spec.Instance.Allocation.CPUCount != int(quantity.Value()) ||
				spec.Instance.Allocation.Threads != 2 ||
				!reflect.DeepEqual(spec.Instance.Allocation.CPUs, []int{0, 1, 2, 3}) ||
				!strings.Contains(strings.Join(spec.Config.RunnerCommand, " "), "--intra-op-threads 2") {
				t.Fatalf("preparation conflated measured request, affinity or threads: %+v", spec)
			}
			inventory := serving.SchedulerNode{
				Node: spec.Instance.Node, CPUs: []int{0, 1, 2, 3}, MemoryBytes: 1 << 30,
			}
			if err = servingkube.ValidatePreparationCapacity(
				"serving", node, &core.PodList{}, inventory, spec.Instance,
			); err != nil {
				t.Fatalf("admitted measured preparation failed capacity: %v", err)
			}
			for _, threads := range []int{0, 5} {
				profile.RuntimeMetadata.IntraOpThreads = threads
				if _, err = service.measuredPreparation(context.Background(), profile, 128<<20); err == nil {
					t.Fatalf("invalid measured thread count %d accepted", threads)
				}
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
		spec, err := (&schedulerService{}).measuredPreparation(context.Background(), profile, 0)
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
	observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence",
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
	store, err := servingkube.NewStore(&rest.Config{Host: "http://127.0.0.1:1"}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	settings := serving.SchedulerSettings{
		ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
		MaxObservationAge: time.Second, MaxReplicas: 1,
		MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
		CapacityMinimumRequests: 1, MaxCPUs: 2,
	}
	// The native store is unreachable, so listing completed measurements fails.
	rejected := schedulerConfiguration{Settings: settings}
	raw, err := json.Marshal(rejected)
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "new"},
		Data:       map[string]string{"scheduler.json": string(raw)},
	})
	observer, err := servingkube.NewObserver(store, kube, execution, &sdk.Client{},
		servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	objects := &stores3.Store{}
	catalog, err := servingkube.NewCatalog(store, objects, 1024, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	profile := measuredAdoptionProfile()
	profile.Condition.LogicalModel = "model"
	observer.SetProfiles([]serving.Profile{profile})
	definition := &serving.Definition{
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
			Profile: profile.ID, Ready: true,
		},
		Profile: &profile, Weight: 1, Capacity: 1,
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
	accepted := &schedulerConfiguration{
		Settings: settings, StaticRouting: map[string]map[string]int{definition.UID: {"physical": 1}},
	}
	service := &schedulerService{
		kube: kube, observer: observer, objects: objects, catalog: catalog, resourceVersion: acceptedVersion,
		sources: profile.ID, staticAppliedRV: acceptedVersion,
		opts: options{servingNamespace: "serving", schedulerConfigMap: "scheduler",
			schedulerConfigKey: "scheduler.json", maxRequestBytes: 1 << 20, maxEvidenceBytes: 1024},
	}
	service.configuration.Store(accepted)
	if err := service.refresh(ctx); err == nil {
		t.Fatal("refresh succeeded without listing completed measurements")
	}
	after, err := execution.State(definition.Name)
	if err != nil || !after.Admission || after.Reason != before.Reason {
		t.Fatalf("failed refresh changed actual runtime admission/routing: before=%+v after=%+v err=%v",
			before, after, err)
	}
	if !reflect.DeepEqual(observer.Profiles(), []serving.Profile{profile}) ||
		service.configuration.Load() != accepted ||
		service.resourceVersion != acceptedVersion || service.staticAppliedRV != acceptedVersion {
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
	definition := &serving.Definition{
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
			Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Profile: profile.ID, Ready: true},
		Profile: &profile, Weight: 1, Capacity: 1,
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
	call, err := service.execution.Accept(receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{
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

func schedulerTestConfiguration() schedulerConfiguration {
	return schedulerConfiguration{
		Settings: serving.SchedulerSettings{
			ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
			MaxObservationAge: time.Second, MaxReplicas: 1,
			MaxInstances: 5, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
			CapacityMinimumRequests: 1, MaxCPUs: 2,
		},
		Reserved: map[string]serving.SchedulerAllocation{"rpi": {CPUs: []int{0}}},
	}
}

func schedulerTestService(t *testing.T, config schedulerConfiguration) *schedulerService {
	t.Helper()
	ctx := context.Background()
	observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence",
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
	observer, err := servingkube.NewObserver(store, kube, execution, &sdk.Client{},
		servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	operations, err := servingkube.NewOperations(store, kube, execution, observer, &sdk.Client{},
		servingkube.OperationConfig{Namespace: "serving", PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	objects := &stores3.Store{}
	catalog, err := servingkube.NewCatalog(store, objects, 1024, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := servingkube.NewSchedulerController(store, kube, execution, observer, operations, observationLog,
		servingkube.SchedulerControllerConfig{Settings: config.Settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	service := &schedulerService{
		kube: kube, store: store, execution: execution, observer: observer, objects: objects,
		catalog: catalog, operations: operations, controller: controller, observationLog: observationLog,
		resourceVersion: "1",
		opts: options{servingNamespace: "serving", schedulerConfigMap: "scheduler", schedulerConfigKey: "scheduler.json",
			profileNamespaces: []string{"measurements"}, maxRequestBytes: 1 << 20, maxEvidenceBytes: 1024},
	}
	service.configuration.Store(&config)
	return service
}
