package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestNativeInstanceServiceLifetimeAndUniqueDestination(t *testing.T) {
	ctx := context.Background()
	first, _, _, _ := observerSourceFixture()
	second := first.DeepCopy()
	second.Name, second.UID = "second-slot", "second-pod-uid"
	second.Labels[instancePodUIDLabel] = string(second.UID)
	second.Status.ContainerStatuses[0].ContainerID = "containerd://second"
	kube := kubefake.NewSimpleClientset(first, second)
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		service := action.(ktesting.CreateAction).GetObject().(*core.Service)
		service.UID, service.ResourceVersion = types.UID(service.Name), "1"
		service.Spec.ClusterIP = "10.96.0.10"
		if service.Name == instanceService(second).Name {
			service.Spec.ClusterIP = "10.96.0.11"
		}
		return false, nil, nil
	})
	operations := &Operations{kube: kube}
	for _, pod := range []*core.Pod{first, second} {
		if err := operations.ensureInstanceService(ctx, pod); err != nil {
			t.Fatal(err)
		}
		if err := operations.ensureInstanceService(ctx, pod); err != nil {
			t.Fatal("matching existing transport was not reusable", err)
		}
	}
	firstEndpoint, err := nativeInstanceEndpoint(ctx, kube, first)
	if err != nil {
		t.Fatal(err)
	}
	secondEndpoint, err := nativeInstanceEndpoint(ctx, kube, second)
	if err != nil || firstEndpoint != "http://10.96.0.10:8080" || secondEndpoint != "http://10.96.0.11:8080" {
		t.Fatal("slots on the same Node did not retain distinct destinations", firstEndpoint, secondEndpoint, err)
	}
	restartable := first.DeepCopy()
	restartable.Spec.RestartPolicy = core.RestartPolicyAlways
	if _, err = nativeInstanceEndpoint(ctx, kube, restartable); err == nil {
		t.Fatal("restartable Pod could alias a replacement executor")
	}
	duplicate := first.DeepCopy()
	duplicate.Name, duplicate.UID = "duplicate", "duplicate-uid"
	if _, err = kube.CoreV1().Pods(first.Namespace).Create(ctx, duplicate, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = nativeInstanceEndpoint(ctx, kube, first); err == nil {
		t.Fatal("a selector matching multiple Pods was accepted")
	}
	if err = kube.CoreV1().Pods(first.Namespace).Delete(ctx, duplicate.Name, meta.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	replacement := first.DeepCopy()
	replacement.Status.ContainerStatuses[0].ContainerID = "containerd://replacement"
	if _, err = nativeInstanceEndpoint(ctx, kube, replacement); err == nil {
		t.Fatal("the Service aliased a different container identity")
	}
	if err = deleteInstanceService(ctx, kube, first); err != nil {
		t.Fatal(err)
	}
	if _, err = kube.CoreV1().Services(first.Namespace).Get(ctx, instanceService(first).Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("removed instance retained its Service", err)
	}
	if _, err = nativeInstanceEndpoint(ctx, kube, second); err != nil {
		t.Fatal("removing one slot removed another slot's transport", err)
	}
}

// m4 r7: the genesis3 prepare canceled at 07:12:30 was waiting in "client
// rate limiter Wait". Each running prepare (rasp5, lattepanda, genesis2 and
// genesis3 at 07:08:31) ran a full observer refresh every poll on top of the
// server's 1 s source refresh, and each refresh lists the Nodes, Pods,
// Deployments, ReplicaSets, classes and actuations and reads every serving
// instance's Service and Pods, all against one 50 QPS client budget. A prepare poll
// reuses a refresh started within its poll interval.
func TestPreparePollsReuseRecentObserverRefresh(t *testing.T) {
	ctx := context.Background()
	manager, _ := newManagerHarness(t)
	pod, node, _, _ := observerSourceFixture()
	kube := kubefake.NewSimpleClientset(node, pod)
	lists := 0
	kube.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		lists++
		return false, nil, nil
	})
	manager.observer.kube = kube
	t.Cleanup(func() { _ = manager.observer.Close() })
	operations := &Operations{store: manager.store, kube: kube, runtime: manager.runtime, observer: manager.observer,
		config: OperationConfig{Namespace: pod.Namespace, PollInterval: time.Minute}}
	if err := manager.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	lists = 0
	for range 4 {
		if _, err := operations.prepareObserved(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
	if lists != 0 {
		t.Fatalf("4 prepare polls listed the Nodes %d times right after a refresh", lists)
	}
}

func TestNativeInstanceServiceRejectsMismatchedExistingResources(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		change func(*core.Service)
		owned  bool
	}{
		{"another Pod owner", func(service *core.Service) { service.OwnerReferences[0].UID = "other-pod" }, false},
		{"shared selector", func(service *core.Service) {
			service.Spec.Selector = map[string]string{"app.kubernetes.io/component": servingInstanceComponent}
		}, true},
		{"missing proxy label", func(service *core.Service) { delete(service.Labels, edgeMeshServiceProxyLabel) }, true},
		{"another target port", func(service *core.Service) { service.Spec.Ports[0].TargetPort = intstr.FromInt32(9090) }, true},
		{"not-ready destinations", func(service *core.Service) { service.Spec.PublishNotReadyAddresses = true }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod, _, _, _ := observerSourceFixture()
			service := observerServiceFixture(pod)
			tc.change(service)
			kube := kubefake.NewSimpleClientset(pod, service)
			operations := &Operations{kube: kube}
			if err := operations.ensureInstanceService(ctx, pod); err == nil {
				t.Fatal("mismatched existing Service was adopted")
			}
			if _, err := nativeInstanceEndpoint(ctx, kube, pod); err == nil {
				t.Fatal("mismatched existing Service became a native destination")
			}
			deleteErr := deleteInstanceService(ctx, kube, pod)
			actual, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, service.Name, meta.GetOptions{})
			if tc.owned {
				if deleteErr != nil || !apierrors.IsNotFound(err) {
					t.Fatal("owned drifted transport blocked cleanup", deleteErr, err)
				}
			} else if deleteErr == nil || err != nil || !reflect.DeepEqual(actual, service) {
				t.Fatal("another Pod's Service was changed or deleted", deleteErr, err)
			}
		})
	}
	// The create race must inspect the winner, not patch it into our binding.
	pod, _, _, _ := observerSourceFixture()
	kube := kubefake.NewSimpleClientset(pod)
	wrong := observerServiceFixture(pod)
	wrong.OwnerReferences[0].UID = "other-pod"
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		if err := kube.Tracker().Add(wrong); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewAlreadyExists(core.Resource("services"), wrong.Name)
	})
	if err := (&Operations{kube: kube}).ensureInstanceService(ctx, pod); err == nil {
		t.Fatal("AlreadyExists race adopted another Pod's Service")
	}
	actualWinner, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, wrong.Name, meta.GetOptions{})
	if err != nil || !reflect.DeepEqual(actualWinner, wrong) {
		t.Fatal("AlreadyExists race changed its winner", err)
	}
	// A stale UID label must not be rewritten into the current identity.
	pod.Labels[instancePodUIDLabel] = "old-pod"
	if err := (&Operations{kube: kube}).ensureInstanceService(ctx, pod); err == nil {
		t.Fatal("mismatched Pod identity label was accepted")
	}
}

func TestExistingPreparationMatchesQuantityValues(t *testing.T) {
	desired, _, _, _ := observerSourceFixture()
	desired.Spec.Containers[0].Resources.Requests[core.ResourceMemory] = *resource.NewQuantity(292552704, resource.BinarySI)
	desired.Spec.Containers[0].Resources.Limits[core.ResourceMemory] = *resource.NewQuantity(292552704, resource.BinarySI)
	for i := range desired.Spec.InitContainers {
		desired.Spec.InitContainers[i].Resources = *desired.Spec.Containers[0].Resources.DeepCopy()
	}
	raw, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	actual := &core.Pod{}
	if err = json.Unmarshal(raw, actual); err != nil {
		t.Fatal(err)
	}
	if !samePreparedPod(actual, desired) {
		t.Fatal("unchanged API-decoded allocation rejected as a different preparation")
	}
	actual.Spec.Containers[0].Resources.Limits[core.ResourceMemory] = resource.MustParse("278Mi")
	if samePreparedPod(actual, desired) {
		t.Fatal("different main memory allocation was reused")
	}
	actual.Spec.Containers[0].Resources.Limits[core.ResourceMemory] = resource.MustParse("279Mi")
	actual.Spec.InitContainers[0].Resources.Limits[core.ResourceCPU] = resource.MustParse("2")
	if samePreparedPod(actual, desired) {
		t.Fatal("different init CPU allocation was reused")
	}
}

func TestPreparationReusesCurrentPolicyAndRepairsExistingAcknowledgment(t *testing.T) {
	testPreparedRouting(t, "")
}

func TestControlledPreparationUsesOnlyUncontrolledOwnerSeeds(t *testing.T) {
	for _, mode := range []string{"manual", "measurement", "planner", "zero", "unacknowledged", "explicit-empty", "explicit-nil", "cleanup"} {
		t.Run(mode, func(t *testing.T) { testPreparedRouting(t, mode) })
	}
}

func testPreparedRouting(t *testing.T, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager, _ := newManagerHarness(t)
	fixture, node, profile, class := observerSourceFixture()
	var owner *serving.WorkloadReference
	if mode != "" {
		if err := manager.runtime.ApplySchedulerTarget(serving.SchedulerCandidate{Routing: map[string]map[string]int32{}}); err != nil {
			t.Fatal(err)
		}
		createManagerCatalog(t, manager)
		workload, err := manager.CreateWorkload(ctx, "prepared-owner", managerWorkloadSpec())
		if err != nil {
			t.Fatal(err)
		}
		owner = &serving.WorkloadReference{Name: workload.Applied.Name, UID: workload.Applied.UID}
		compatible := managerProfile(workload.Applied)
		profile.Condition.LogicalModel = compatible.Condition.LogicalModel
		profile.Condition.Artifact = compatible.Condition.Artifact
		profile.Condition.Preprocessor = compatible.Condition.Preprocessor
		profile.Condition.ActiveProtocol = compatible.Condition.ActiveProtocol
		profile.Condition.RuntimeFamily = compatible.Condition.RuntimeFamily
		profile.Condition.Backend = compatible.Condition.Backend
		profile.Condition.Firstfloor = compatible.Condition.Firstfloor
		if mode == "explicit-empty" || mode == "explicit-nil" || mode == "cleanup" {
			distribution := map[string]int32{}
			if mode == "explicit-nil" {
				distribution = nil
			}
			if err := manager.runtime.ApplySchedulerTarget(serving.SchedulerCandidate{
				Routing: map[string]map[string]int32{owner.UID: distribution},
			}); err != nil {
				t.Fatal(err)
			}
			if mode == "cleanup" {
				if len(manager.runtime.Instances()) != 0 || len(manager.runtime.SchedulerInputs(time.Now).Outstanding) != 0 {
					t.Fatal("cleanup configuration requires an idle empty physical pool")
				}
				controller := &SchedulerController{runtime: manager.runtime, config: SchedulerControllerConfig{
					StaticRouting: true,
					Settings: serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second,
						ComparisonPeriod: time.Second, MaxObservationAge: time.Second, MaxReplicas: 1, MaxInstances: 1,
						CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1,
						CapacityMinimumRequests: 1, PlanSearchWorkers: 1, MaxCPUs: 4},
				}}
				cleared := controller.config
				cleared.StaticRouting = false
				if err := controller.SetConfiguration(cleared); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if mode == "measurement" {
		node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Effect: core.TaintEffectNoSchedule}}
	}
	profile.RuntimeMetadata.RuntimeVersion = "test-runtime"
	profile.RuntimeMetadata.Inputs = profile.Condition.Preprocessor.Inputs
	profile.RuntimeMetadata.Outputs = profile.Condition.Preprocessor.Outputs
	profile.RuntimeMetadata.InitializedBatches = []int{1}
	manager.observer.SetProfiles([]serving.Profile{profile})
	kube := kubefake.NewSimpleClientset(node)
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		service := action.(ktesting.CreateAction).GetObject().(*core.Service)
		service.Spec.ClusterIP = "10.96.0.21"
		return false, nil, nil
	})
	// Share API state between the uncached writer and native observer.
	manager.store.client = fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithStatusSubresource(&edge.OperationCostMeasurement{}).WithObjects(&class, node.DeepCopy()).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			if pod, ok := object.(*core.Pod); ok {
				pod.UID = fixture.UID
				pod.Labels[instancePodUIDLabel] = string(pod.UID)
				pod.Status = fixture.Status
			}
			if err := c.Create(ctx, object, opts...); err != nil {
				return err
			}
			if pod, ok := object.(*core.Pod); ok {
				return kube.Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace)
			}
			return nil
		},
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if err := c.Patch(ctx, object, patch, opts...); err != nil {
				return err
			}
			if pod, ok := object.(*core.Pod); ok {
				return kube.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace)
			}
			return nil
		},
	}).Build()
	currentPolicy := native.Policy{DecisionID: "manual-static-routing:current", ProfileID: profile.ID, MaxBatchSize: 1, MaxBatchWaitMS: 17}
	actualRuntime := *profile.RuntimeMetadata
	actualRuntime.ModelFiles = deliveredModelFiles(profile.Condition.Artifact.Files)
	executions := make(chan native.Request, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/infer") {
			var request native.Request
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			executions <- request
			_ = json.NewEncoder(w).Encode(native.Response{ID: request.ID, ModelName: "chill_model",
				Outputs: []native.Field{{Name: "label", Data: [][]byte{[]byte("ok")}}}, ExecutionComplete: true,
				Progress: native.Progress{RequestID: request.ID, WorkloadUID: request.Context.WorkloadUID,
					Definition: request.Context.Definition, ProfileID: profile.ID, Phase: "finished", At: time.Now(),
					BatchID: 1, BatchSize: 1, Units: 1, ExecutionComplete: true}})
			return
		}
		// Native state reports the mounted policy, never the Pod annotation.
		_ = json.NewEncoder(w).Encode(native.State{At: time.Now().UTC(), Ready: mode != "unacknowledged", Metadata: native.Metadata{
			Model: "chill_model", Mode: "serving", ProfileID: profile.ID, DecisionID: currentPolicy.DecisionID,
			Policy: currentPolicy, AppliedAt: time.Now().UTC(), Runtime: actualRuntime,
			MaxQueueRequests: int(profile.Condition.Firstfloor.MaxQueueSize), MaxBatchSize: 1, ModelInstanceCount: 1,
		}})
	}))
	defer server.Close()
	manager.observer.kube = kube
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	manager.observer.native.HTTPClient = &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(proxy),
	}}
	operations := &Operations{store: manager.store, kube: kube, runtime: manager.runtime, observer: manager.observer,
		config: OperationConfig{Namespace: fixture.Namespace, PollInterval: time.Millisecond, Measurement: mode == "measurement"}}
	spec := PrepareSpec{Name: fixture.Name, MemoryBytes: 1024, Owner: owner,
		Instance: serving.SchedulerInstance{ID: "slot", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile, Weight: 1,
			Allocation: serving.SchedulerAllocation{CPUs: []int{0}, Threads: 1, MemoryBytes: 1024}},
		Store:  storekube.Binding{Store: "evidence", Endpoint: "http://evidence", Bucket: "bucket", Region: "region", CredentialsSecretName: "credentials"},
		Bundle: evidencestore.ObjectIdentity{Store: "evidence", Key: "bundle", Size: 1},
		Config: native.Config{Mode: "serving", Model: "chill_model", ProfilePath: "/profile/bundle.json",
			Policy: native.Policy{DecisionID: "bootstrap", ProfileID: profile.ID, MaxBatchSize: 1}},
	}
	if mode == "planner" {
		spec.Instance.WorkloadUID = owner.UID
	}
	if mode == "zero" {
		spec.Instance.Weight = 0
	}
	persisted := spec
	persisted.Config.Policy = currentPolicy
	if err := bindPreparationAffinity(&persisted); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.ensurePreparationConfig(ctx, persisted, nil); err != nil {
		t.Fatal(err)
	}
	if mode == "unacknowledged" || mode == "zero" {
		prepareCtx, stop := context.WithTimeout(ctx, 30*time.Millisecond)
		defer stop()
		if err := operations.PrepareInstance(prepareCtx, spec); err == nil {
			t.Fatalf("%s preparation unexpectedly became ready", mode)
		}
		if routing, _ := manager.runtime.SchedulerRouting(); len(routing[owner.UID]) != 0 {
			t.Fatalf("%s preparation installed a route: %v", mode, routing)
		}
		return
	}
	// Recreate after routing has changed the retained ConfigMap's mutable policy.
	if err := operations.PrepareInstance(ctx, spec); err != nil {
		t.Fatal("recreated executor could not acknowledge its mounted current policy", err)
	}
	pod := &core.Pod{}
	key := types.NamespacedName{Namespace: fixture.Namespace, Name: fixture.Name}
	if err := manager.store.client.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	pod.Annotations[ServingPolicyAnnotation] = "bootstrap"
	if err := manager.store.client.Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := kube.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace); err != nil {
		t.Fatal(err)
	}
	if err := manager.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, instance := range manager.runtime.Instances() {
		if instance.Ready {
			t.Fatal("stale bootstrap annotation incorrectly acknowledged the current native policy")
		}
	}
	if err := operations.PrepareInstance(ctx, spec); err != nil {
		t.Fatal("idempotent prepare did not repair a previous partial acknowledgment", err)
	}
	cm := &core.ConfigMap{}
	if err := manager.store.client.Get(ctx, key, cm); err != nil {
		t.Fatal(err)
	}
	var config native.Config
	if err := json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.Policy, currentPolicy) {
		t.Fatal("preparation reset the current mutable policy", config.Policy)
	}
	if owner != nil {
		physical := physicalID(fixture.UID, firstfloorStatus(fixture).ContainerID)
		routing, _ := manager.runtime.SchedulerRouting()
		if mode == "planner" || mode == "zero" || mode == "explicit-empty" || mode == "explicit-nil" {
			receipt, err := manager.runtime.Receive(owner.Name, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			call, err := manager.runtime.Accept(receipt, managerSample(t))
			if err != nil {
				t.Fatal(err)
			}
			pending := manager.runtime.SchedulerInputs(time.Now).Outstanding
			if len(routing[owner.UID]) != 0 || len(pending) != 1 || pending[0].Instance != "" || len(executions) != 0 {
				t.Fatalf("%s preparation joined routing: %v %+v", mode, routing, pending)
			}
			canceled, stop := context.WithCancel(ctx)
			stop()
			_, _ = call.Wait(canceled)
			if err := manager.runtime.Response(receipt, "canceled", "RequestCanceled"); err != nil {
				t.Fatal(err)
			}
			return
		}
		if !reflect.DeepEqual(routing[owner.UID], map[string]int32{physical: 1}) {
			t.Fatalf("prepared route did not name exact physical executor: %v", routing)
		}
		if err := manager.observer.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		receipt, err := manager.runtime.Receive(owner.Name, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		call, err := manager.runtime.Accept(receipt, managerSample(t))
		if err != nil {
			t.Fatal(err)
		}
		result, err := call.Wait(ctx)
		if err != nil || !result.Finished || result.StatusCode != http.StatusOK {
			t.Fatalf("prepared request did not execute through native client: %+v %v", result, err)
		}
		if len(executions) != 1 {
			t.Fatalf("native executions = %d, want 1", len(executions))
		}
		if err := manager.runtime.Response(receipt, "ok", ""); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Run("OperationCostPinsPreparedPhysicalIdentity", func(t *testing.T) {
		operations.native = manager.observer.native
		controller := &SchedulerController{store: manager.store}
		idle := int32(1)
		m := opcostRoot("physical-cost", time.Now(),
			edge.OperationCostStep{Name: "prepare", Kind: edge.OperationCostStepPrepare, BatchEnergyOptimization: "beo"},
			edge.OperationCostStep{Name: "idle", Kind: edge.OperationCostStepIdle, IdleDurationSeconds: &idle},
			edge.OperationCostStep{Name: "remove", Kind: edge.OperationCostStepRemove, BatchEnergyOptimization: "beo"})
		if err := manager.store.client.Create(ctx, m); err != nil {
			t.Fatal(err)
		}
		physical := physicalID(fixture.UID, firstfloorStatus(fixture).ContainerID)
		var removed string
		controller.measureStep = func(ctx context.Context, _ SchedulerControllerConfig, request MeasurementRequest) (OperationMeasurement, evidencestore.Receipt, error) {
			record := OperationMeasurement{Verdict: measurementAccepted}
			switch request.Operation.Kind {
			case operationPrepare:
				result, err := operations.Execute(ctx, request.Operation)
				if err != nil {
					t.Fatal(err)
				}
				record.Result = result
				actual := schedulerPhysicalState(spec.Instance.ID, result.Actual.Instances)
				if actual == nil || actual.ID != physical {
					t.Fatalf("prepare did not observe physical executor: %+v", result.Actual.Instances)
				}
			case operationRemove:
				removed = request.Operation.Instance
				// Exercise the production boundary against the prepared Pod, not a
				// synthetic result or an alias-tolerant removal substitute.
				if err := operations.validateOperationBinding(ctx, request.Operation); err != nil {
					t.Fatal(err)
				}
			}
			return record, evidencestore.Receipt{}, nil
		}
		config := SchedulerControllerConfig{Preparations: map[string]PrepareSpec{profile.ID: spec}}
		if err := controller.runOperationCost(ctx, m, spec.Instance.Node, nil, map[string]string{"beo": profile.ID}, config); err != nil {
			t.Fatal(err)
		}
		if removed != physical || m.Status.Phase != edge.MeasurementPhaseComplete {
			t.Fatalf("remove instance %q, want %q; status %+v", removed, physical, m.Status)
		}
	})
}

func TestCUDAPreparationReproducesMeasuredSupplementalGroups(t *testing.T) {
	ctx := context.Background()
	_, node, profile, _ := observerSourceFixture()
	profile.Condition.RuntimeAccess = &profilederivation.CurveRuntimeAccessContract{SupplementalGroups: []int64{44}}
	var err error
	scheme := runtime.NewScheme()
	if err = core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := kubefake.NewSimpleClientset(node)
	// Stop at the transport Service so the test inspects only the created Pod.
	kube.PrependReactor("create", "services", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("stop after Pod creation")
	})
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(node.DeepCopy()).Build()}
	operations := &Operations{store: store, kube: kube, config: OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond}}
	spec := PrepareSpec{
		Name: "measured-rknn",
		Instance: serving.SchedulerInstance{
			ID: "measured-rknn", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile,
			Allocation: serving.SchedulerAllocation{CPUs: []int{0}, CPUCount: 1, Threads: 1, MemoryBytes: 1 << 20},
		},
		Bundle: evidencestore.ObjectIdentity{Store: "store", Key: "bundle", Size: 1},
		Store:  storekube.Binding{Store: "store", Endpoint: "http://store", Bucket: "bucket", Region: "region", CredentialsSecretName: "secret"},
		Config: native.Config{
			Mode: nativeServingMode, ProfilePath: "/profile/bundle.json",
			RunnerCommand: []string{"python3", "--delivery-json", "/config/delivery.json"},
			Policy:        native.Policy{ProfileID: profile.ID, DeadlinePolicy: profile.Condition.Firstfloor.DeadlinePolicy},
		},
		MemoryBytes: 1 << 20,
	}
	if err = operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "stop after Pod creation") {
		t.Fatalf("preparation did not reach Pod creation: %v", err)
	}
	pod := &core.Pod{}
	if err = store.client.Get(ctx, types.NamespacedName{Namespace: "chill-system", Name: spec.Name}, pod); err != nil {
		t.Fatal(err)
	}
	if pod.Labels[profilingrun.MeasurementWorkloadLabelKey] != profilingrun.ServingInstanceRole {
		t.Fatalf("Pod labels = %#v", pod.Labels)
	}
	if pod.Spec.SecurityContext == nil || !reflect.DeepEqual(pod.Spec.SecurityContext.SupplementalGroups, []int64{44}) {
		t.Fatalf("measured supplemental groups were not reproduced: %+v", pod.Spec.SecurityContext)
	}
}

// A new Pod is not routed until a route records its distribution; a manual
// preparation serves only its owner.
func TestPreparationSeedsRoutingBeforeRoute(t *testing.T) {
	ctx := context.Background()
	_, node, profile, _ := observerSourceFixture()
	scheme := runtime.NewScheme()
	if err := core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := kubefake.NewSimpleClientset(node)
	kube.PrependReactor("create", "services", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("stop after Pod creation")
	})
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(node.DeepCopy()).Build()}
	operations := &Operations{store: store, kube: kube, config: OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond}}
	for _, owner := range []*serving.WorkloadReference{nil, {Name: "owner", UID: "owner-uid"}} {
		name, want := "scheduler-prepared", `{}`
		if owner != nil {
			name, want = "manual-prepared", `{"owner-uid":2}`
		}
		spec := PrepareSpec{Name: name, Owner: owner, MemoryBytes: 1 << 20,
			Instance: serving.SchedulerInstance{ID: name, Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile, Weight: 2,
				Allocation: serving.SchedulerAllocation{CPUs: []int{0}, CPUCount: 1, Threads: 1, MemoryBytes: 1 << 20}},
			Bundle: evidencestore.ObjectIdentity{Store: "store", Key: "bundle", Size: 1},
			Store:  storekube.Binding{Store: "store", Endpoint: "http://store", Bucket: "bucket", Region: "region", CredentialsSecretName: "secret"},
			Config: native.Config{Mode: nativeServingMode, ProfilePath: "/profile/bundle.json",
				Policy: native.Policy{ProfileID: profile.ID, DeadlinePolicy: profile.Condition.Firstfloor.DeadlinePolicy}},
		}
		if err := operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "stop after Pod creation") {
			t.Fatalf("%s preparation did not reach Pod creation: %v", name, err)
		}
		pod := &core.Pod{}
		if err := store.client.Get(ctx, types.NamespacedName{Namespace: "chill-system", Name: name}, pod); err != nil {
			t.Fatal(err)
		}
		if got := pod.Annotations[ServingRoutingAnnotation]; got != want {
			t.Fatalf("%s Pod routing %q, want %q", name, got, want)
		}
	}
}

func TestRKNNPreparationProjectsMeasuredHostAccess(t *testing.T) {
	ctx := context.Background()
	_, node, profile, _ := observerSourceFixture()
	profile.Condition.Backend = profilingrun.FirstfloorRKNNLiteBackend
	profile.RuntimeMetadata.IntraOpThreads, profile.RuntimeMetadata.InterOpThreads = 0, 0
	delivery, err := json.Marshal(profilingrun.CandidateRuntimeDelivery{
		ArtifactCapability: &profilingrun.CandidateArtifactCapability{},
		HostContract: &profilingrun.CandidateReportHostContract{
			Libraries: []profilingrun.CandidateReportHostLibrary{{
				Name: "librknnrt", HostPath: "/usr/lib/librknnrt.so", ContainerPath: "/usr/lib/librknnrt.so",
			}},
			Driver:   profilingrun.CandidateReportHostDriver{Name: "rknpu", VersionPath: "/sys/kernel/debug/rknpu/version", Version: "0.9.8"},
			Devices:  []profilingrun.CandidateReportHostDevice{{Path: "/dev/dri/renderD129"}},
			Platform: &profilingrun.CandidateReportHostPlatform{CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588"},
		},
		RuntimeAccess: &profilingrun.CandidateReportRuntimeAccess{Privileged: true, SupplementalGroups: []int64{44, 110}},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile.RuntimeDelivery = delivery
	scheme := runtime.NewScheme()
	if err = core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := kubefake.NewSimpleClientset(node)
	// Stop at the transport Service so the test inspects only the created Pod.
	kube.PrependReactor("create", "services", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("stop after Pod creation")
	})
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(node.DeepCopy()).Build()}
	operations := &Operations{store: store, kube: kube, config: OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond}}
	spec := PrepareSpec{
		Name: "measured-rknn",
		Instance: serving.SchedulerInstance{
			ID: "measured-rknn", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile,
			Allocation: serving.SchedulerAllocation{CPUs: []int{0}, CPUCount: 1, MemoryBytes: 1 << 20},
		},
		Bundle: evidencestore.ObjectIdentity{Store: "store", Key: "bundle", Size: 1},
		Store:  storekube.Binding{Store: "store", Endpoint: "http://store", Bucket: "bucket", Region: "region", CredentialsSecretName: "secret"},
		Config: native.Config{
			Mode: nativeServingMode, ProfilePath: "/profile/bundle.json",
			RunnerCommand: []string{"python3", "--delivery-json", "/config/delivery.json"},
			Policy:        native.Policy{ProfileID: profile.ID, DeadlinePolicy: profile.Condition.Firstfloor.DeadlinePolicy},
		},
		MemoryBytes: 1 << 20,
	}
	if err = operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "stop after Pod creation") {
		t.Fatalf("preparation did not reach Pod creation: %v", err)
	}
	config := &core.ConfigMap{}
	if err = store.client.Get(ctx, types.NamespacedName{Namespace: "chill-system", Name: spec.Name}, config); err != nil {
		t.Fatal(err)
	}
	if config.Data["delivery.json"] != string(delivery) {
		t.Fatal("runner delivery file differs from the measured delivery")
	}
	pod := &core.Pod{}
	if err = store.client.Get(ctx, types.NamespacedName{Namespace: "chill-system", Name: spec.Name}, pod); err != nil {
		t.Fatal(err)
	}
	if pod.Labels[profilingrun.MeasurementWorkloadLabelKey] != profilingrun.ServingInstanceRole {
		t.Fatalf("Pod labels = %#v", pod.Labels)
	}
	container := pod.Spec.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.Privileged == nil || !*container.SecurityContext.Privileged ||
		pod.Spec.SecurityContext == nil || !reflect.DeepEqual(pod.Spec.SecurityContext.SupplementalGroups, []int64{44, 110}) {
		t.Fatalf("measured privilege or supplemental groups were not reproduced: %+v %+v", container.SecurityContext, pod.Spec.SecurityContext)
	}
	mounted := map[string]string{}
	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath == nil {
			continue
		}
		for _, mount := range container.VolumeMounts {
			if mount.Name == volume.Name {
				mounted[mount.MountPath] = volume.HostPath.Path
			}
		}
	}
	for _, path := range []string{"/usr/lib/librknnrt.so", "/sys/kernel/debug/rknpu/version", "/proc/device-tree/compatible", "/dev/dri/renderD129"} {
		if mounted[path] != path {
			t.Fatalf("measured host path %s was not projected: %v", path, mounted)
		}
	}
	// The unchanged existing Pod is reused; one with another host source, group
	// set or privilege is a different preparation.
	if err = operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "stop after Pod creation") {
		t.Fatalf("unchanged existing preparation was not reused: %v", err)
	}
	original := pod.DeepCopy()
	for name, mutate := range map[string]func(*core.Pod){
		"host source":         func(p *core.Pod) { p.Spec.Volumes[len(p.Spec.Volumes)-1].HostPath.Path = "/dev/dri/renderD128" },
		"supplemental groups": func(p *core.Pod) { p.Spec.SecurityContext.SupplementalGroups = []int64{44} },
		"privilege":           func(p *core.Pod) { p.Spec.Containers[0].SecurityContext.Privileged = ptr.To(false) },
	} {
		existing := &core.Pod{}
		if err = store.client.Get(ctx, types.NamespacedName{Namespace: "chill-system", Name: spec.Name}, existing); err != nil {
			t.Fatal(err)
		}
		existing.Spec = *original.Spec.DeepCopy()
		mutate(existing)
		if err = store.client.Update(ctx, existing); err != nil {
			t.Fatal(err)
		}
		if err = operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "existing preparation Pod differs") {
			t.Fatalf("existing Pod with different %s was reused: %v", name, err)
		}
	}
}

func TestMeasurementPoolAdmitsOnlyMatchingOperations(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name                 string
		measurement, tainted bool
		admitted             bool
	}{
		{name: "workload operation on measurement Node", tainted: true},
		{name: "measurement operation on untainted Node", measurement: true},
		{name: "measurement operation on measurement Node", measurement: true, tainted: true, admitted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, node, _, _ := observerSourceFixture()
			if tc.tainted {
				node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Effect: core.TaintEffectNoSchedule}}
			}
			scheme := runtime.NewScheme()
			if err := core.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			kube := kubefake.NewSimpleClientset(node)
			// Stop at the transport Service so the test inspects only the created Pod.
			kube.PrependReactor("create", "services", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("stop after Pod creation")
			})
			store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(node.DeepCopy()).Build()}
			operations := &Operations{store: store, kube: kube, config: OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond, Measurement: tc.measurement}}
			spec := measuredCPUPreparationFixture()
			err := operations.PrepareInstance(ctx, spec)
			pod := &core.Pod{}
			getErr := store.client.Get(ctx, types.NamespacedName{Namespace: "chill-system", Name: spec.Name}, pod)
			if !tc.admitted {
				if err == nil || errors.Is(err, ErrNodeInMeasurement) == tc.measurement || !apierrors.IsNotFound(getErr) {
					t.Fatalf("prepare admitted or created a Pod: err=%v get=%v", err, getErr)
				}
				if !tc.measurement {
					// observer nil: reaching any effect past admission panics.
					if err = (&Operations{store: store}).ChangePower(ctx, spec.Instance.Node, "", lowMode, time.Minute); !errors.Is(err, ErrNodeInMeasurement) {
						t.Fatalf("power change on measurement Node: %v", err)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "stop after Pod creation") || getErr != nil {
				t.Fatalf("measurement preparation did not reach Pod creation: %v %v", err, getErr)
			}
			if !slices.Contains(pod.Spec.Tolerations, profilingrun.MeasurementToleration()) {
				t.Fatalf("measurement Pod lacks the measurement toleration: %+v", pod.Spec.Tolerations)
			}
		})
	}
}

// The measured power change runs on the empty measurement Node like the measured prepare.
func TestMeasuredPowerChangeOnEmptyMeasurementNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager, _ := newManagerHarness(t)
	_, node, _, class := observerSourceFixture()
	node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Effect: core.TaintEffectNoSchedule}}
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	actuator := func(id int32) edge.PowerModeActuator {
		return edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: id}}
	}
	class.Spec.PowerModes = []edge.PowerMode{{Name: lowMode, Actuator: actuator(0)}, {Name: "boost", Actuator: actuator(1)}}
	node.Annotations[chillmeta.PowerActuator] = nvpModel0
	kube := kubefake.NewSimpleClientset(node)
	// The fake agent settles every requested mode at once.
	manager.store.client = &nodeAPI{Client: fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithObjects(&class).Build(), kube: kube, agent: settleAgent(&class)}
	manager.observer.kube = kube
	operations, err := NewOperations(manager.store, kube, manager.runtime, manager.observer, manager.observer.native,
		OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond, Measurement: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := operations.Execute(ctx, Operation{ID: "raise", Kind: operationPower, Node: ref, PowerMode: "boost", MaxActuationDuration: time.Minute})
	if err != nil || result.Status != nativeOperationComplete {
		t.Fatalf("measured power change on the measurement Node: %v %+v", err, result)
	}
	from, to, err := operationCostKeys(result)
	if err != nil || from != "eco" || to != "boost" || len(result.Before.States) != 0 {
		t.Fatalf("empty-Node transition keys %q->%q %v %+v", from, to, err, result.Before)
	}
}
