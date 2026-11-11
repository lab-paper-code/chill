package kube

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/test/fixtures"
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
	second.Labels[servingstate.InstancePodUIDLabel] = string(second.UID)
	second.Status.ContainerStatuses[0].ContainerID = "containerd://second"
	kube := kubefake.NewSimpleClientset(first, second)
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		service := action.(ktesting.CreateAction).GetObject().(*core.Service)
		service.UID, service.ResourceVersion = types.UID(service.Name), "1"
		service.Spec.ClusterIP = "10.96.0.10"
		if service.Name == servingstate.InstanceService(second).Name {
			service.Spec.ClusterIP = "10.96.0.11"
		}
		return false, nil, nil
	})
	operations := &servingops.Operations{Kube: kube}
	for _, pod := range []*core.Pod{first, second} {
		if err := operations.EnsureInstanceService(ctx, pod); err != nil {
			t.Fatal(err)
		}
		if err := operations.EnsureInstanceService(ctx, pod); err != nil {
			t.Fatal("matching existing transport was not reusable", err)
		}
	}
	firstEndpoint, err := servingstate.NativeInstanceEndpoint(ctx, kube, first)
	if err != nil {
		t.Fatal(err)
	}
	secondEndpoint, err := servingstate.NativeInstanceEndpoint(ctx, kube, second)
	if err != nil || firstEndpoint != "http://10.96.0.10:8080" || secondEndpoint != "http://10.96.0.11:8080" {
		t.Fatal("slots on the same Node did not retain distinct destinations", firstEndpoint, secondEndpoint, err)
	}
	restartable := first.DeepCopy()
	restartable.Spec.RestartPolicy = core.RestartPolicyAlways
	if _, err = servingstate.NativeInstanceEndpoint(ctx, kube, restartable); err == nil {
		t.Fatal("restartable Pod could alias a replacement executor")
	}
	duplicate := first.DeepCopy()
	duplicate.Name, duplicate.UID = "duplicate", "duplicate-uid"
	if _, err = kube.CoreV1().Pods(first.Namespace).Create(ctx, duplicate, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = servingstate.NativeInstanceEndpoint(ctx, kube, first); err == nil {
		t.Fatal("a selector matching multiple Pods was accepted")
	}
	if err = kube.CoreV1().Pods(first.Namespace).Delete(ctx, duplicate.Name, meta.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	replacement := first.DeepCopy()
	replacement.Status.ContainerStatuses[0].ContainerID = "containerd://replacement"
	if _, err = servingstate.NativeInstanceEndpoint(ctx, kube, replacement); err == nil {
		t.Fatal("the Service aliased a different container identity")
	}
	if err = servingstate.DeleteInstanceService(ctx, kube, first); err != nil {
		t.Fatal(err)
	}
	if _, err = kube.CoreV1().Services(first.Namespace).Get(ctx, servingstate.InstanceService(first).Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("removed instance retained its Service", err)
	}
	if _, err = servingstate.NativeInstanceEndpoint(ctx, kube, second); err != nil {
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
	operations := &servingops.Operations{Cluster: manager.observer.cluster, Store: manager.store, Kube: kube, Runtime: manager.runtime, Observer: manager.observer, Config: servingops.OperationConfig{Namespace: pod.Namespace, PollInterval: time.Minute}}
	if err := manager.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	lists = 0
	for range 4 {
		if _, err := operations.PrepareObserved(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
	if lists != 0 {
		t.Fatalf("4 prepare polls listed the Nodes %d times right after a refresh", lists)
	}
}

const otherPodUID = "other-pod"

func TestNativeInstanceServiceRejectsMismatchedExistingResources(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		change func(*core.Service)
		owned  bool
	}{
		{"another Pod owner", func(service *core.Service) { service.OwnerReferences[0].UID = otherPodUID }, false},
		{"shared selector", func(service *core.Service) {
			service.Spec.Selector = map[string]string{"app.kubernetes.io/component": servingstate.ServingInstanceComponent}
		}, true},
		{"missing proxy label", func(service *core.Service) { delete(service.Labels, servingstate.EdgeMeshServiceProxyLabel) }, true},
		{"another target port", func(service *core.Service) { service.Spec.Ports[0].TargetPort = intstr.FromInt32(9090) }, true},
		{"not-ready destinations", func(service *core.Service) { service.Spec.PublishNotReadyAddresses = true }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod, _, _, _ := observerSourceFixture()
			service := observerServiceFixture(pod)
			tc.change(service)
			kube := kubefake.NewSimpleClientset(pod, service)
			operations := &servingops.Operations{Kube: kube}
			if err := operations.EnsureInstanceService(ctx, pod); err == nil {
				t.Fatal("mismatched existing Service was adopted")
			}
			if _, err := servingstate.NativeInstanceEndpoint(ctx, kube, pod); err == nil {
				t.Fatal("mismatched existing Service became a native destination")
			}
			deleteErr := servingstate.DeleteInstanceService(ctx, kube, pod)
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
	wrong.OwnerReferences[0].UID = otherPodUID
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		if err := kube.Tracker().Add(wrong); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewAlreadyExists(core.Resource("services"), wrong.Name)
	})
	if err := (&servingops.Operations{Kube: kube}).EnsureInstanceService(ctx, pod); err == nil {
		t.Fatal("AlreadyExists race adopted another Pod's Service")
	}
	actualWinner, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, wrong.Name, meta.GetOptions{})
	if err != nil || !reflect.DeepEqual(actualWinner, wrong) {
		t.Fatal("AlreadyExists race changed its winner", err)
	}
	// A stale UID label must not be rewritten into the current identity.
	pod.Labels[servingstate.InstancePodUIDLabel] = "old-pod"
	if err := (&servingops.Operations{Kube: kube}).EnsureInstanceService(ctx, pod); err == nil {
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
	if !servingops.SamePreparedPod(actual, desired) {
		t.Fatal("unchanged API-decoded allocation rejected as a different preparation")
	}
	actual.Spec.Containers[0].Resources.Limits[core.ResourceMemory] = resource.MustParse("278Mi")
	if servingops.SamePreparedPod(actual, desired) {
		t.Fatal("different main memory allocation was reused")
	}
	actual.Spec.Containers[0].Resources.Limits[core.ResourceMemory] = resource.MustParse("279Mi")
	actual.Spec.InitContainers[0].Resources.Limits[core.ResourceCPU] = resource.MustParse("2")
	if servingops.SamePreparedPod(actual, desired) {
		t.Fatal("different init CPU allocation was reused")
	}
}

func TestPreparationReusesCurrentPolicyAndRepairsExistingAcknowledgment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager, _ := newManagerHarness(t)
	fixture, node, profile, class := observerSourceFixture()
	profile.Condition.Preprocessor.Inputs = []string{"input"}
	profile.Condition.Preprocessor.Outputs = []string{"output"}
	profile.Condition.Actor.MemoryRequestBytes = 128 << 20
	profile.Condition.Actor.MemoryLimitBytes = 1 << 30
	profile.Batches[0].MemoryBytes = 256 << 20
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
	manager.store.client = fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithStatusSubresource(&edge.OperationCostMeasurement{}).WithObjects(testExecutionFacts(), &class, node.DeepCopy()).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			if pod, ok := object.(*core.Pod); ok {
				pod.UID = fixture.UID
				pod.Labels[servingstate.InstancePodUIDLabel] = string(pod.UID)
				pod.Status = fixture.Status
			}
			if err := c.Create(ctx, object, opts...); err != nil {
				return err
			}
			if pod, ok := object.(*core.Pod); ok {
				if err := kube.Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace); err != nil {
					return err
				}
				// The fake client-go log stream ends immediately. Keep an explicit
				// subscribed source so EOF cannot masquerade as runner termination.
				follow, stop := context.WithCancel(manager.observer.ctx)
				manager.observer.mu.Lock()
				manager.observer.sources[string(pod.UID)] = &nativeSource{
					pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name},
					uid: pod.UID, containerID: servingstate.FirstfloorStatus(pod).ContainerID, follow: follow, cancel: stop,
				}
				manager.observer.mu.Unlock()
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
	currentPolicy := native.Policy{DecisionID: "manual-static-routing:current", ProfileID: profile.ID, MaxBatchSize: 1, MaxBatchWaitMS: 17, DeadlinePolicy: native.DeadlineDrop}
	manager.observer.kube = kube
	operations := &servingops.Operations{Cluster: manager.observer.cluster, Store: manager.store, Kube: kube, Runtime: manager.runtime, Observer: manager.observer, Config: servingops.OperationConfig{Namespace: fixture.Namespace, PollInterval: time.Millisecond}}
	spec := servingstate.PrepareSpec{Name: fixture.Name, MemoryBytes: 1 << 30,
		Instance: serving.SchedulerInstance{ID: "slot", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile, Weight: 1,
			Allocation: serving.SchedulerAllocation{MemoryBytes: 1 << 30}},
		Store:  storekube.Binding{Store: "evidence", Endpoint: "http://evidence", Bucket: "bucket", Region: "region", CredentialsSecretName: "credentials"},
		Bundle: evidencestore.ObjectIdentity{Store: "evidence", Key: "bundle", Size: 1},
		Config: native.Config{Mode: "serving", Model: profile.Condition.Actor.Model, ProfilePath: "/profile/bundle.json",
			RuntimeImage: profile.Condition.RuntimeImage, MemoryRequestBytes: 128 << 20, MemoryLimitBytes: 1 << 30,
			ArtifactImage: profile.Condition.Artifact.Image, ArtifactFiles: profile.Condition.Artifact.Files,
			PreprocessorImage: profile.Condition.Preprocessor.Image, PreprocessorPath: profile.Condition.Preprocessor.Path,
			Policy: native.Policy{DecisionID: "bootstrap", ProfileID: profile.ID, MaxBatchSize: 1}},
	}
	persisted := spec
	persisted.Config.Policy = currentPolicy
	nativeHTTPClient, actorPolicy := preparationNativeHTTPClient(t, ctx, persisted.Config, profile)
	manager.observer.native.HTTPClient = nativeHTTPClient
	if _, err := operations.EnsurePreparationConfig(ctx, persisted, nil); err != nil {
		t.Fatal(err)
	}
	// Recreate after routing has changed the retained ConfigMap's mutable policy.
	if _, err := operations.PrepareInstance(ctx, spec); err != nil {
		t.Fatal("recreated executor could not acknowledge its mounted current policy", err)
	}
	pod := &core.Pod{}
	key := types.NamespacedName{Namespace: fixture.Namespace, Name: fixture.Name}
	if err := manager.store.client.Get(ctx, key, pod); err != nil {
		t.Fatal(err)
	}
	operations.Native = manager.observer.native
	assertPreparedInstanceObserved(t, ctx, operations, pod, spec.Instance.Node)
	pod.Annotations[servingstate.ServingPolicyAnnotation] = "bootstrap"
	if err := manager.store.client.Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := kube.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace); err != nil {
		t.Fatal(err)
	}
	if err := manager.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	id := servingstate.PhysicalID(fixture.UID, servingstate.FirstfloorStatus(fixture).ContainerID)
	assertCurrentPolicyObservation(t, manager.runtime.Instances(), id, policyAckPendingReason, actorPolicy)
	if _, err := operations.PrepareInstance(ctx, spec); err != nil {
		t.Fatal("idempotent prepare did not repair a previous partial acknowledgment", err)
	}
	if err := manager.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	assertCurrentPolicyObservation(t, manager.runtime.Instances(), id, "", actorPolicy)
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
	t.Run("OperationCostPinsPreparedPhysicalIdentity", func(t *testing.T) {
		assertOperationCostPreparedPhysicalIdentity(t, ctx, manager, operations, spec, id)
	})
	t.Run("ObserverRejectsInsufficientActualLimit", func(t *testing.T) {
		current, err := kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		current.Spec.Containers[0].Resources.Limits[core.ResourceMemory] = resource.MustParse("128Mi")
		if _, err := kube.CoreV1().Pods(pod.Namespace).Update(ctx, current, meta.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := manager.observer.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if ready, err := operations.PrepareObserved(ctx, current); err != nil || ready {
			t.Fatalf("actual limit128Mi below peak256Mi became Ready: %v %v", ready, err)
		}
		current.Spec.Containers[0].Resources.Limits[core.ResourceMemory] = resource.MustParse("1Gi")
		if _, err := kube.CoreV1().Pods(pod.Namespace).Update(ctx, current, meta.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := manager.observer.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("DrainUsesMountedCommonModel", func(t *testing.T) {
		deleted := errors.New("stop after native drain proof")
		manager.store.client = interceptor.NewClient(manager.store.client.(client.WithWatch), interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
				if _, ok := object.(*core.Pod); ok {
					return deleted
				}
				return c.Delete(ctx, object, opts...)
			},
		})
		if err := operations.RemoveInstance(ctx, spec.Instance.Node, id); !errors.Is(err, deleted) {
			t.Fatalf("removal did not pass the real native model's drain proof: %v", err)
		}
	})
}

// A startup-only IPC peer supplies explicit fixture readback to the real actor.
// The parent process never sends inference; cancellation reaps this child.
func TestNativeStateRunnerProcess(t *testing.T) {
	raw := os.Getenv("CHILL_NATIVE_STATE_RUNNER")
	if raw == "" {
		return
	}
	var size uint32
	if err := binary.Read(os.Stdin, binary.BigEndian, &size); err != nil {
		os.Exit(2)
	}
	if _, err := io.CopyN(io.Discard, os.Stdin, int64(size)); err != nil {
		os.Exit(2)
	}
	frame, err := json.Marshal(map[string]any{"type": "ready", "data_bytes": 0, "metadata": json.RawMessage(raw)})
	if err != nil || binary.Write(os.Stdout, binary.BigEndian, uint32(len(frame))) != nil {
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(frame); err != nil {
		os.Exit(2)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func preparationNativeHTTPClient(t *testing.T, ctx context.Context, config native.Config, profile serving.Profile) (*http.Client, native.Policy) {
	t.Helper()
	actualRuntime := *profile.RuntimeMetadata
	actualRuntime.ModelFiles = deliveredModelFiles(profile.Condition.Artifact.Files)
	runtimeJSON, err := json.Marshal(actualRuntime)
	if err != nil {
		t.Fatal(err)
	}
	actorConfig := config
	actorConfig.Inputs, actorConfig.Outputs = profile.Condition.Preprocessor.Inputs, profile.Condition.Preprocessor.Outputs
	actorConfig.BatchSizes, actorConfig.MaxBatchSize = []int{1}, 1
	actorConfig.MaxQueueRequests, actorConfig.MaxQueueBytes, actorConfig.MaxRequestBytes = 4, 1<<20, 1<<20
	actorConfig.RunnerTimeoutMS = 3000
	actorConfig.RunnerCommand = []string{"env", "CHILL_NATIVE_STATE_RUNNER=" + string(runtimeJSON), os.Args[0], "-test.run=^TestNativeStateRunnerProcess$"}
	actorConfig.Policy.Batches = []native.BatchProfile{{Size: 1, ServiceSamples: []native.ServiceSample{fixtures.ServiceSample("physical-trial", 0, 1000000), fixtures.ServiceSample("physical-trial", 1, 1000000)}, EnergyPerItemJ: 1}}
	if err := native.ValidateConfig(&actorConfig); err != nil {
		t.Fatal(err)
	}
	// Run the production HTTP handler and actor; only the idle IPC runner's
	// initialization metadata is a fixture (no hardware inference is claimed).
	actorContext, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	runner, err := native.StartRunner(actorContext, &actorConfig)
	if err != nil {
		t.Fatal(err)
	}
	actor := native.NewScheduler(actorContext, &actorConfig, runner)
	t.Cleanup(func() {
		cancel()
		<-actor.Done()
	})
	server := httptest.NewServer(native.NewHandler(actor))
	t.Cleanup(server.Close)
	if _, err := (&sdk.Client{}).State(ctx, sdk.Spec{Endpoint: server.URL, ModelName: "chill_model"}); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("old hardcoded model unexpectedly reached the native actor: %v", err)
	}
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Immutable batch samples are startup-only (Policy.Batches is json:"-");
	// the native HTTP ACK carries only the applied mutable policy.
	ackPolicy := actorConfig.Policy
	ackPolicy.Batches = nil
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}, ackPolicy
}

func assertPreparedInstanceObserved(t *testing.T, ctx context.Context, operations *servingops.Operations, pod *core.Pod, node serving.NodeReference) {
	t.Helper()
	for _, containers := range [][]core.Container{pod.Spec.Containers, pod.Spec.InitContainers} {
		for _, container := range containers {
			if container.Resources.Requests.Memory().Value() != 128<<20 || container.Resources.Limits.Memory().Value() != 1<<30 {
				t.Fatalf("preparation changed the common scheduling request/runtime limit: %#v", container.Resources)
			}
		}
	}
	if ready, err := operations.PrepareObserved(ctx, pod); err != nil || !ready {
		t.Fatalf("request128Mi < peak256Mi < limit1Gi did not become Ready: %v %v", ready, err)
	}
	captured, err := operations.Capture(ctx, node)
	if err != nil || len(captured.Native) != 1 || len(captured.Reasons) != 0 {
		t.Fatalf("capture did not use the mounted common model: %+v %v", captured, err)
	}
}

func assertOperationCostPreparedPhysicalIdentity(t *testing.T, ctx context.Context, manager *managerHarness, operations *servingops.Operations, spec servingstate.PrepareSpec, physical string) {
	t.Helper()
	operations.Native = manager.observer.native
	controller := &SchedulerController{reservationState: &reservationState{}, store: manager.store}
	idle := int32(1)
	m := opcostRoot("physical-cost", time.Now(),
		edge.OperationCostStep{Name: "prepare", Kind: edge.OperationCostStepPrepare, BatchEnergyOptimization: "beo"},
		edge.OperationCostStep{Name: "idle", Kind: edge.OperationCostStepIdle, IdleDurationSeconds: &idle},
		edge.OperationCostStep{Name: "remove", Kind: edge.OperationCostStepRemove, BatchEnergyOptimization: "beo"})
	if err := manager.store.client.Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	var removed string
	controller.measureStep = func(ctx context.Context, _ SchedulerControllerConfig, request servingops.MeasurementRequest) (servingops.OperationMeasurement, evidencestore.Receipt, error) {
		record := servingops.OperationMeasurement{Verdict: servingstate.MeasurementAccepted}
		switch request.Operation.Kind {
		case servingstate.OperationPrepare:
			result, err := operations.Execute(ctx, request.Operation)
			if err != nil {
				t.Fatal(err)
			}
			record.Result = result
			actual := schedulerPhysicalState(spec.Instance.ID, result.Actual.Instances)
			if actual == nil || actual.ID != physical {
				t.Fatalf("prepare did not observe physical executor: %+v", result.Actual.Instances)
			}
		case servingstate.OperationRemove:
			removed = request.Operation.Instance
			// Exercise the production boundary against the prepared Pod, not a
			// synthetic result or an alias-tolerant removal substitute.
			if err := operations.ValidateOperationBinding(ctx, request.Operation); err != nil {
				t.Fatal(err)
			}
		}
		return record, evidencestore.Receipt{}, nil
	}
	profileID := spec.Instance.Profile.ID
	config := SchedulerControllerConfig{Preparations: map[string]servingstate.PrepareSpec{profileID: spec}}
	if err := controller.runOperationCost(ctx, m, spec.Instance.Node, nil, map[string]string{"beo": profileID}, config); err != nil {
		t.Fatal(err)
	}
	if removed != physical || m.Status.Phase != edge.MeasurementPhaseComplete {
		t.Fatalf("remove instance %q, want %q; status %+v", removed, physical, m.Status)
	}
}

func TestCUDAPreparationReproducesMeasuredSupplementalGroups(t *testing.T) {
	ctx := context.Background()
	_, node, profile, _ := observerSourceFixture()
	profile.Condition.RuntimeAccess = &profilederivation.CurveRuntimeAccessContract{SupplementalGroups: []int64{44}}
	desiredImage := "registry/native@sha256:" + strings.Repeat("c", 64)
	profile.Condition.RuntimeImage = "registry/native@sha256:" + strings.Repeat("b", 64)
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
	observer := newTestObserver(t)
	operations := &servingops.Operations{Store: store, Kube: kube, Runtime: observer.runtime, Observer: observer, Config: servingops.OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond}}
	spec := servingstate.PrepareSpec{
		Name: "measured-rknn",
		Instance: serving.SchedulerInstance{
			ID: "measured-rknn", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile,
			Allocation: serving.SchedulerAllocation{MemoryBytes: 1 << 20},
		},
		Bundle: evidencestore.ObjectIdentity{Store: "store", Key: "bundle", Size: 1},
		Store:  storekube.Binding{Store: "store", Endpoint: "http://store", Bucket: "bucket", Region: "region", CredentialsSecretName: "secret"},
		Config: native.Config{
			RuntimeImage: desiredImage, MemoryRequestBytes: 1 << 20, MemoryLimitBytes: 1 << 20,
			ArtifactImage: profile.Condition.Artifact.Image, ArtifactFiles: profile.Condition.Artifact.Files,
			PreprocessorImage: profile.Condition.Preprocessor.Image, PreprocessorPath: profile.Condition.Preprocessor.Path,
			Mode: servingstate.NativeServingMode, ProfilePath: "/profile/bundle.json",
			RunnerCommand: []string{"python3", "--delivery-json", "/config/delivery.json"},
			Policy:        native.Policy{ProfileID: profile.ID},
		},
		MemoryBytes: 1 << 20,
	}
	if _, err = operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "stop after Pod creation") {
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
	if pod.Spec.Containers[0].Image != desiredImage || pod.Spec.InitContainers[2].Image != desiredImage || profile.Condition.RuntimeImage == desiredImage {
		t.Fatal("prepare did not retain the frozen Config image for worker/materializer")
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
	observer := newTestObserver(t)
	operations := &servingops.Operations{Store: store, Kube: kube, Runtime: observer.runtime, Observer: observer, Config: servingops.OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond}}
	for _, owner := range []*serving.WorkloadReference{nil, {Name: "owner", UID: "owner-uid"}} {
		name, want := "scheduler-prepared", `{}`
		if owner != nil {
			name, want = "manual-prepared", `{"owner-uid":2}`
		}
		spec := servingstate.PrepareSpec{Name: name, Owner: owner, MemoryBytes: 1 << 20,
			Instance: serving.SchedulerInstance{ID: name, Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile, Weight: 2,
				Allocation: serving.SchedulerAllocation{MemoryBytes: 1 << 20}},
			Bundle: evidencestore.ObjectIdentity{Store: "store", Key: "bundle", Size: 1},
			Store:  storekube.Binding{Store: "store", Endpoint: "http://store", Bucket: "bucket", Region: "region", CredentialsSecretName: "secret"},
			Config: native.Config{Mode: servingstate.NativeServingMode, ProfilePath: "/profile/bundle.json",
				RuntimeImage: profile.Condition.RuntimeImage, MemoryRequestBytes: 1 << 20, MemoryLimitBytes: 1 << 20,
				ArtifactImage: profile.Condition.Artifact.Image, ArtifactFiles: profile.Condition.Artifact.Files,
				PreprocessorImage: profile.Condition.Preprocessor.Image, PreprocessorPath: profile.Condition.Preprocessor.Path,
				Policy: native.Policy{ProfileID: profile.ID}},
		}
		if _, err := operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "stop after Pod creation") {
			t.Fatalf("%s preparation did not reach Pod creation: %v", name, err)
		}
		pod := &core.Pod{}
		if err := store.client.Get(ctx, types.NamespacedName{Namespace: "chill-system", Name: name}, pod); err != nil {
			t.Fatal(err)
		}
		if got := pod.Annotations[servingstate.ServingRoutingAnnotation]; got != want {
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
	observer := newTestObserver(t)
	operations := &servingops.Operations{Store: store, Kube: kube, Runtime: observer.runtime, Observer: observer, Config: servingops.OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond}}
	spec := servingstate.PrepareSpec{
		Name: "measured-rknn",
		Instance: serving.SchedulerInstance{
			ID: "measured-rknn", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile,
			Allocation: serving.SchedulerAllocation{MemoryBytes: 1 << 20},
		},
		Bundle: evidencestore.ObjectIdentity{Store: "store", Key: "bundle", Size: 1},
		Store:  storekube.Binding{Store: "store", Endpoint: "http://store", Bucket: "bucket", Region: "region", CredentialsSecretName: "secret"},
		Config: native.Config{
			RuntimeImage: profile.Condition.RuntimeImage, MemoryRequestBytes: 1 << 20, MemoryLimitBytes: 1 << 20,
			ArtifactImage: profile.Condition.Artifact.Image, ArtifactFiles: profile.Condition.Artifact.Files,
			PreprocessorImage: profile.Condition.Preprocessor.Image, PreprocessorPath: profile.Condition.Preprocessor.Path,
			Mode: servingstate.NativeServingMode, ProfilePath: "/profile/bundle.json",
			RunnerCommand: []string{"python3", "--delivery-json", "/config/delivery.json"},
			Policy:        native.Policy{ProfileID: profile.ID},
		},
		MemoryBytes: 1 << 20,
	}
	if _, err = operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "stop after Pod creation") {
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
	if _, err = operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "stop after Pod creation") {
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
		if _, err = operations.PrepareInstance(ctx, spec); err == nil || !strings.Contains(err.Error(), "existing preparation Pod differs") {
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
			observer := newTestObserver(t)
			operations := &servingops.Operations{Store: store, Kube: kube, Runtime: observer.runtime, Observer: observer, Config: servingops.OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond, Measurement: tc.measurement}}
			spec := measuredCPUPreparationFixture()
			evidence, err := operations.PrepareInstance(ctx, spec)
			pod := &core.Pod{}
			getErr := store.client.Get(ctx, types.NamespacedName{Namespace: "chill-system", Name: spec.Name}, pod)
			if !tc.admitted {
				if err == nil || errors.Is(err, servingops.ErrNodeInMeasurement) == tc.measurement || !apierrors.IsNotFound(getErr) {
					t.Fatalf("prepare admitted or created a Pod: err=%v get=%v", err, getErr)
				}
				if !tc.measurement {
					// observer nil: reaching any effect past admission panics.
					if err = (&servingops.Operations{Store: store, Runtime: observer.runtime}).ChangePower(ctx, spec.Instance.Node, "", lowMode, time.Minute); !errors.Is(err, servingops.ErrNodeInMeasurement) {
						t.Fatalf("power change on measurement Node: %v", err)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "stop after Pod creation") || getErr != nil {
				t.Fatalf("measurement preparation did not reach Pod creation: %v %v", err, getErr)
			}
			if evidence == nil || !evidence.Created || evidence.PodUID != string(pod.UID) ||
				evidence.NodeUID != spec.Instance.Node.UID || evidence.DecisionID == "" {
				t.Fatalf("post-Create failure lost independent API evidence: %+v", evidence)
			}
			reused, reuseErr := operations.PrepareInstance(ctx, spec)
			if reuseErr == nil || reused == nil || reused.Created || reused.PodUID != evidence.PodUID {
				t.Fatalf("verified existing GET lost reused provenance: %+v %v", reused, reuseErr)
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
	manager.store.client = &nodeAPI{Client: fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithObjects(testExecutionFacts(), &class).Build(), kube: kube, agent: settleAgent(&class)}
	manager.observer.kube = kube
	operations, err := servingops.NewOperations(manager.reservations, manager.store, kube, manager.runtime, manager.observer, manager.observer.native,
		servingops.OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond, Measurement: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := operations.Execute(ctx, servingstate.Operation{ID: "raise", Kind: servingstate.OperationPower, Node: ref, PowerMode: "boost", MaxActuationDuration: time.Minute})
	if err != nil || result.Status != servingstate.NativeOperationComplete {
		t.Fatalf("measured power change on the measurement Node: %v %+v", err, result)
	}
	from, to, err := servingops.OperationCostKeys(result)
	if err != nil || from != "eco" || to != "boost" || len(result.Before.States) != 0 {
		t.Fatalf("empty-Node transition keys %q->%q %v %+v", from, to, err, result.Before)
	}
}

func assertCurrentPolicyObservation(t *testing.T, instances []serving.InstanceState, id, reason string, policy native.Policy) {
	t.Helper()
	if len(instances) != 1 {
		t.Fatalf("expected one actual executor, got %+v", instances)
	}
	instance := instances[0]
	if instance.ID != id || !instance.Ready || instance.Reason != reason || instance.Native == nil ||
		instance.Native.Metadata.DecisionID != policy.DecisionID || !reflect.DeepEqual(instance.Native.Metadata.Policy, policy) {
		t.Fatalf("annotation discarded readiness or fabricated native ACK: %+v", instance)
	}
}

type createdPodFailureObservation struct {
	serving.RuntimeObservation
	err error
}

func (o createdPodFailureObservation) RecordRecovery(state serving.InstanceState, clear bool) error {
	if state.CreatedThisRun {
		return o.err
	}
	return o.RuntimeObservation.RecordRecovery(state, clear)
}

func TestPreparationCreationEvidencePrecedesSourcePublication(t *testing.T) {
	for _, alreadyExists := range []bool{false, true} {
		name := "successful create"
		if alreadyExists {
			name = "AlreadyExists race"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			manager, _ := newManagerHarness(t)
			creationFailure := errors.New("cannot admit new Pod provenance")
			rt, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 8,
				ObservationLog: createdPodFailureObservation{RuntimeObservation: manager.observationLog, err: creationFailure}})
			if err != nil {
				t.Fatal(err)
			}
			fixture, node, _, _ := observerSourceFixture()
			kube := kubefake.NewSimpleClientset(node)
			serviceBoundary := errors.New("stop after source publication")
			kube.PrependReactor("create", "services", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, serviceBoundary
			})
			store := &Store{client: fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithObjects(node.DeepCopy()).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, cl client.WithWatch, object client.Object, opts ...client.CreateOption) error {
						pod, isPod := object.(*core.Pod)
						if isPod {
							pod.UID = fixture.UID
							pod.Status = fixture.Status
						}
						if err := cl.Create(ctx, object, opts...); err != nil {
							return err
						}
						if !isPod {
							return nil
						}
						if err := kube.Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace); err != nil {
							return err
						}
						if alreadyExists {
							// Model another creator winning before our API response.
							return apierrors.NewAlreadyExists(core.Resource("pods"), pod.Name)
						}
						return nil
					},
				}).Build()}
			observer := newTestObserver(t)
			observer.runtime = rt
			operations := &servingops.Operations{Store: store, Kube: kube, Runtime: rt, Observer: observer,
				Config: servingops.OperationConfig{Namespace: fixture.Namespace, PollInterval: time.Millisecond}}
			spec := measuredCPUPreparationFixture()
			evidence, err := operations.PrepareInstance(ctx, spec)
			pod := &core.Pod{}
			if getErr := store.client.Get(ctx, types.NamespacedName{Namespace: fixture.Namespace, Name: spec.Name}, pod); getErr != nil {
				t.Fatal("preparation lost the created remote effect", getErr)
			}
			if evidence == nil || evidence.PodUID != string(pod.UID) || evidence.Created == alreadyExists {
				t.Fatalf("preparation lost actual creation ownership: %+v", evidence)
			}
			published := slices.ContainsFunc(rt.Instances(), func(state serving.InstanceState) bool { return state.PodUID == string(pod.UID) })
			if alreadyExists {
				if !errors.Is(err, serviceBoundary) || !published {
					t.Fatalf("existing Pod was incorrectly treated as newly created: err=%v published=%t", err, published)
				}
			} else if !errors.Is(err, creationFailure) || published {
				t.Fatalf("failed provenance admission published the new source: err=%v published=%t", err, published)
			}
		})
	}
}
