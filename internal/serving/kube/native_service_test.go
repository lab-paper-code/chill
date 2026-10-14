package kube

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
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
