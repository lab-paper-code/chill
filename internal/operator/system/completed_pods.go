package system

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
)

// cleanupCompletedDeploymentPods retains failed Pods for logs and leaves Job
// evidence consumption to each Job's controller.
func (r *ChillSystemReconciler) cleanupCompletedDeploymentPods(ctx context.Context, system *edgev1alpha1.ChillSystem) error {
	namespaces := [2]string{r.managementNamespace(system), r.namespace()}
	for i, namespace := range namespaces {
		if namespace == "" || (i > 0 && namespace == namespaces[0]) {
			continue
		}
		pods := &corev1.PodList{}
		if err := r.List(ctx, pods, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("list completed Deployment Pods in %s: %w", namespace, err)
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			if pod.Status.Phase != corev1.PodSucceeded || !pod.DeletionTimestamp.IsZero() {
				continue
			}
			owner := metav1.GetControllerOf(pod)
			if owner == nil || owner.APIVersion != appsv1.SchemeGroupVersion.String() || owner.Kind != "ReplicaSet" || owner.UID == "" {
				continue
			}
			rs := &appsv1.ReplicaSet{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: owner.Name}, rs); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("get completed Pod ReplicaSet: %w", err)
			}
			if !metav1.IsControlledBy(pod, rs) {
				continue
			}
			owner = metav1.GetControllerOf(rs)
			if owner == nil || owner.APIVersion != appsv1.SchemeGroupVersion.String() || owner.Kind != "Deployment" || owner.UID == "" {
				continue
			}
			deployment := &appsv1.Deployment{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: owner.Name}, deployment); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("get completed Pod Deployment: %w", err)
			}
			if !metav1.IsControlledBy(rs, deployment) || !r.isSystemDeployment(system, deployment) {
				continue
			}
			// Do not delete a replacement Pod or a Pod changed since observation.
			if err := r.Delete(ctx, pod, client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("delete completed Deployment Pod %s/%s: %w", namespace, pod.Name, err)
			}
		}
	}
	return nil
}

func (r *ChillSystemReconciler) isSystemDeployment(system *edgev1alpha1.ChillSystem, deployment *appsv1.Deployment) bool {
	labels := deployment.Labels
	if labels["app.kubernetes.io/name"] == component.DefaultSystemName && labels["app.kubernetes.io/instance"] == system.Name {
		owner := metav1.GetControllerOf(deployment)
		if owner != nil && owner.APIVersion == edgev1alpha1.GroupVersion.String() && owner.Kind == "ChillSystem" && system.UID != "" && metav1.IsControlledBy(deployment, system) {
			switch labels["app.kubernetes.io/component"] {
			case component.Profiler, component.EvidenceStore:
				return true
			}
		}
	}
	// Helm operands have no ChillSystem owner reference. Bind their release
	// identity to the chart labels on the root, including nameOverride.
	if deployment.Namespace != r.managementNamespace(system) || system.Labels["app.kubernetes.io/instance"] == "" || system.Labels["app.kubernetes.io/name"] == "" || !strings.HasPrefix(system.Labels["helm.sh/chart"], "chill-") || system.Labels["app.kubernetes.io/managed-by"] != "Helm" {
		return false
	}
	for _, key := range []string{"app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/managed-by"} {
		if labels[key] != system.Labels[key] {
			return false
		}
	}
	if !strings.HasPrefix(labels["helm.sh/chart"], "chill-") {
		return false
	}
	switch labels["app.kubernetes.io/component"] {
	case "operator", "query", "dashboard", "serving-entrypoint", "evaluation-store":
		return true
	default:
		return false
	}
}
