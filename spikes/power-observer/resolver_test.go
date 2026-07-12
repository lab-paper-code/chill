package powerobserverprobe

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testSelector = "app.kubernetes.io/name=exporter,app.kubernetes.io/instance=edge-metrics"

func TestKubernetesPodResolverResolvesOneReadyPod(t *testing.T) {
	client := fake.NewSimpleClientset(sourcePod("exporter-ready", true, false))
	resolver, err := NewKubernetesPodResolver(client, "monitoring", testSelector, 9102)
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolver.Resolve(context.Background(), "lattepanda")
	if err != nil {
		t.Fatal(err)
	}
	if target.Endpoint != "http://155.230.34.144:9102/metrics" || target.PodName != "exporter-ready" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestKubernetesPodResolverReportsUnavailableNotReadyAndAmbiguous(t *testing.T) {
	tests := []struct {
		name string
		pods []*corev1.Pod
		want error
	}{
		{name: "unavailable", want: ErrSourceUnavailable},
		{name: "not ready", pods: []*corev1.Pod{sourcePod("not-ready", false, false)}, want: ErrSourceNotReady},
		{
			name: "ambiguous",
			pods: []*corev1.Pod{sourcePod("one", true, false), sourcePod("two", true, false)},
			want: ErrSourceAmbiguous,
		},
		{
			name: "ignore terminating during rollout",
			pods: []*corev1.Pod{sourcePod("old", true, true), sourcePod("new", true, false)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			for _, pod := range test.pods {
				_, err := client.CoreV1().Pods("monitoring").Create(
					context.Background(), pod, metav1.CreateOptions{},
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			resolver, err := NewKubernetesPodResolver(client, "monitoring", testSelector, 9102)
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.Resolve(context.Background(), "lattepanda")
			if test.want == nil && err != nil {
				t.Fatal(err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func sourcePod(name string, ready, terminating bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "monitoring",
			Labels: map[string]string{"app.kubernetes.io/name": "exporter", "app.kubernetes.io/instance": "edge-metrics"},
		},
		Spec: corev1.PodSpec{NodeName: "lattepanda"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, PodIP: "155.230.34.144",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
		},
	}
	if ready {
		pod.Status.Conditions[0].Status = corev1.ConditionTrue
	}
	if terminating {
		now := metav1.NewTime(time.Now())
		pod.DeletionTimestamp = &now
		pod.Finalizers = []string{"test"}
	}
	return pod
}
