package kube

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	operations := &Operations{store: store, kube: kube, config: OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond}}
	spec := PrepareSpec{
		Name: "measured-rknn",
		Instance: serving.SchedulerInstance{
			ID: "measured-rknn", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Pool: "pool", Profile: &profile,
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
