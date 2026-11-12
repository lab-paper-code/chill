package kube

import (
	"context"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGatewayLoadOriginRequiresActualReadyInstallationPod(t *testing.T) {
	for _, mode := range []string{"ready", "unready", "same-node", "ambiguous", "wrong-installation"} {
		t.Run(mode, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			system := &api.ChillSystem{ObjectMeta: metav1.ObjectMeta{Name: "system-name", Labels: map[string]string{"app.kubernetes.io/instance": "release-name"}}}
			gateway := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "system", UID: "gateway-uid", Labels: map[string]string{
				"app.kubernetes.io/instance": "release-name", "app.kubernetes.io/component": "serving-entrypoint",
			}}, Spec: corev1.PodSpec{NodeName: "actual-gateway-node"}, Status: corev1.PodStatus{Phase: corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
			if mode == "unready" {
				gateway.Status.Conditions = nil
			}
			if mode == "same-node" {
				gateway.Spec.NodeName = "measured"
			}
			if mode == "wrong-installation" {
				gateway.Labels["app.kubernetes.io/instance"] = "another-release"
			}
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, gateway,
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "arbitrary-other-node"}})
			if mode == "ambiguous" {
				other := gateway.DeepCopy()
				other.Name, other.UID, other.Spec.NodeName = "other-gateway", "other-uid", "another-node"
				builder = builder.WithObjects(other)
			}
			node, pod, err := ResolveGatewayLoadOrigin(context.Background(), builder.Build(), "system", system.Name, "measured")
			if mode != "ready" {
				if err == nil {
					t.Fatalf("accepted unproven gateway origin %q %+v", node, pod)
				}
				return
			}
			if err != nil || node != gateway.Spec.NodeName || pod.Name != gateway.Name || pod.Namespace != gateway.Namespace || pod.UID != string(gateway.UID) {
				t.Fatalf("lost actual gateway origin: node=%q pod=%+v err=%v", node, pod, err)
			}
		})
	}
}
