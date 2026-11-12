package kube

import (
	"context"
	"errors"
	"fmt"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResolveGatewayLoadOrigin uses the gateway Pod's actual placement, just as
// dispatch occupancy uses ChillServer's downward-API NODE_NAME.
func ResolveGatewayLoadOrigin(ctx context.Context, reader client.Reader, namespace, systemName, measurementNode string) (string, profilingrun.LoadOriginGatewayPodContract, error) {
	empty := profilingrun.LoadOriginGatewayPodContract{}
	system := &api.ChillSystem{}
	if err := reader.Get(ctx, client.ObjectKey{Name: systemName}, system); err != nil {
		return "", empty, fmt.Errorf("read profiling installation identity: %w", err)
	}
	instance := system.Labels["app.kubernetes.io/instance"]
	if instance == "" {
		return "", empty, errors.New("ChillSystem has no installation instance label")
	}
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.InNamespace(namespace), client.MatchingLabels{
		"app.kubernetes.io/instance": instance, "app.kubernetes.io/component": "serving-entrypoint",
	}); err != nil {
		return "", empty, fmt.Errorf("list actual serving gateway Pods: %w", err)
	}
	var gateway *corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning || pod.Spec.NodeName == "" || pod.UID == "" {
			continue
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			continue
		}
		if gateway != nil {
			return "", empty, errors.New("serving gateway load origin is ambiguous: multiple Ready Pods")
		}
		gateway = pod
	}
	if gateway == nil {
		return "", empty, errors.New("serving gateway load origin has no Ready Pod")
	}
	if gateway.Spec.NodeName == measurementNode {
		return "", empty, errors.New("serving gateway load origin is the measurement Node")
	}
	return gateway.Spec.NodeName, profilingrun.LoadOriginGatewayPodContract{
		Namespace: gateway.Namespace, Name: gateway.Name, UID: string(gateway.UID),
	}, nil
}
