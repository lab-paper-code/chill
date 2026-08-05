package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesWorkloadSafetyCheckerAllowsOnlyConfiguredNamespaces(t *testing.T) {
	t.Parallel()
	request := leaseTestRequest("handoff-a")
	checker := workloadSafetyChecker(t,
		workloadPod("kube-system", "kube-proxy", "system-uid", request.Node.Name, corev1.PodRunning),
		workloadPod("chill-system", "node-actuator", "actuator-uid", request.Node.Name, corev1.PodRunning),
		workloadPod("chill-system", "other", "other-chill-uid", request.Node.Name, corev1.PodRunning),
		workloadPod("tenant-b", "finished", "finished-uid", request.Node.Name, corev1.PodSucceeded),
		workloadPod("tenant-a", "serving", "serving-uid", request.Node.Name, corev1.PodRunning),
		workloadPod("tenant-a", "other-node", "other-uid", "jetsony", corev1.PodRunning),
	)
	wantTime := time.Date(2026, 8, 6, 12, 0, 5, 0, time.UTC)
	checker.now = func() time.Time { return wantTime }
	result, err := checker.Check(context.Background(), request)
	if err != nil || result.Allowed || !result.ObservedAt.Equal(wantTime) || len(result.BlockingPods) != 2 ||
		result.BlockingPods[0].Namespace != "chill-system" || result.BlockingPods[0].Name != "other" ||
		result.BlockingPods[1].Namespace != "tenant-a" || result.BlockingPods[1].Name != "serving" {
		t.Fatalf("workload safety result = %#v, error=%v", result, err)
	}
}

func TestKubernetesWorkloadSafetyCheckerReturnsAllowedForExactApprovedSet(t *testing.T) {
	t.Parallel()
	request := leaseTestRequest("handoff-a")
	checker := workloadSafetyChecker(t,
		workloadPod("kube-system", "kube-proxy", "system-uid", request.Node.Name, corev1.PodRunning),
		workloadPod("chill-system", "node-actuator", "actuator-uid", request.Node.Name, corev1.PodRunning),
	)
	result, err := checker.Check(context.Background(), request)
	if err != nil || !result.Allowed || len(result.BlockingPods) != 0 || result.ObservedAt.IsZero() {
		t.Fatalf("allowed workload result = %#v, error=%v", result, err)
	}
}

func TestKubernetesWorkloadSafetyCheckerFailsClosedOnUnrepresentableSet(t *testing.T) {
	t.Parallel()
	request := leaseTestRequest("handoff-a")
	pods := make([]client.Object, 0, maximumRecordedBlockingPods+1)
	for i := 0; i <= maximumRecordedBlockingPods; i++ {
		pods = append(pods, workloadPod(
			"tenant-a", fmt.Sprintf("serving-%02d", i), fmt.Sprintf("uid-%02d", i),
			request.Node.Name, corev1.PodRunning,
		))
	}
	checker := workloadSafetyChecker(t, pods...)
	if _, err := checker.Check(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "exceeding") {
		t.Fatalf("unrepresentable blocker error = %v", err)
	}
}

func TestWorkloadSafetyOptionsRequiresExplicitUniqueNamespaces(t *testing.T) {
	t.Parallel()
	for _, options := range []WorkloadSafetyOptions{
		{},
		{AllowedNamespaces: []string{"Invalid Namespace"}},
		{AllowedNamespaces: []string{"chill-system", "chill-system"}},
		{AllowedNamespaces: []string{"kube-system"}, AllowedPodUIDs: []string{"pod", "pod"}},
	} {
		if err := options.Validate(); err == nil {
			t.Fatalf("invalid safety options accepted: %#v", options)
		}
	}
}

func workloadSafetyChecker(t *testing.T, objects ...client.Object) *KubernetesWorkloadSafetyChecker {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithIndex(
		&corev1.Pod{},
		"spec.nodeName",
		func(object client.Object) []string {
			return []string{object.(*corev1.Pod).Spec.NodeName}
		},
	).Build()
	checker, err := NewKubernetesWorkloadSafetyChecker(reader, WorkloadSafetyOptions{
		AllowedNamespaces: []string{"kube-system"}, AllowedPodUIDs: []string{"actuator-uid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return checker
}

func workloadPod(
	namespace, name, uid, node string,
	phase corev1.PodPhase,
) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID(uid)},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: phase},
	}
}
