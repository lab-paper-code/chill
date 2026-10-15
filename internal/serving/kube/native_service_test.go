package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager, _ := newManagerHarness(t)
	fixture, node, profile, class := observerSourceFixture()
	profile.RuntimeMetadata.RuntimeVersion = "test-runtime"
	profile.RuntimeMetadata.Inputs = []native.InputSpec{}
	profile.RuntimeMetadata.InitializedBatches = []int{1}
	manager.observer.SetProfiles([]serving.Profile{profile})
	kube := kubefake.NewSimpleClientset(node)
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		service := action.(ktesting.CreateAction).GetObject().(*core.Service)
		service.Spec.ClusterIP = "10.96.0.21"
		return false, nil, nil
	})
	// Share API state between the uncached writer and native observer.
	manager.store.client = fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithObjects(&class).WithInterceptorFuncs(interceptor.Funcs{
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
	currentPolicy := native.Policy{DecisionID: "manual-static-routing:current", ProfileID: profile.ID, MaxBatchSize: 1, CompletionOverheadMS: 17}
	actualRuntime := *profile.RuntimeMetadata
	actualRuntime.ModelPath = "/model/model.onnx"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Native state reports the mounted policy, never the Pod annotation.
		_ = json.NewEncoder(w).Encode(native.State{At: time.Now().UTC(), Ready: true, Metadata: native.Metadata{
			Model: "chill_model", Mode: "serving", ProfileID: profile.ID, DecisionID: currentPolicy.DecisionID,
			Policy: currentPolicy, AppliedAt: time.Now().UTC(), Runtime: actualRuntime,
			MaxQueueRequests: 4, MaxBatchSize: 1, ModelInstanceCount: 1,
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
		config: OperationConfig{Namespace: fixture.Namespace, PollInterval: time.Millisecond}}
	spec := PrepareSpec{Name: fixture.Name, MemoryBytes: 1024,
		Instance: serving.SchedulerInstance{ID: "slot", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Pool: "pool", Profile: &profile, Weight: 1,
			Allocation: serving.SchedulerAllocation{CPUs: []int{0}, Threads: 1, MemoryBytes: 1024}},
		Store:  storekube.Binding{Store: "evidence", Endpoint: "http://evidence", Bucket: "bucket", Region: "region", CredentialsSecretName: "credentials"},
		Bundle: evidencestore.ObjectIdentity{Store: "evidence", Key: "bundle", Size: 1},
		Config: native.Config{Mode: "serving", Model: "chill_model", ProfilePath: "/profile/bundle.json",
			Policy: native.Policy{DecisionID: "bootstrap", ProfileID: profile.ID, MaxBatchSize: 1}},
	}
	persisted := spec
	persisted.Config.Policy = currentPolicy
	if err := bindPreparationAffinity(&persisted); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.ensurePreparationConfig(ctx, persisted); err != nil {
		t.Fatal(err)
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
	for _, pool := range manager.runtime.Pools() {
		for _, instance := range pool.Instances {
			if instance.Ready {
				t.Fatal("stale bootstrap annotation incorrectly acknowledged the current native policy")
			}
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
}
