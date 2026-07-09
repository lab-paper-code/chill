package discovery

import (
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operator/watch"
)

// SetupWithManager sets up node and catalog watches for device discovery.
func (r *DeviceDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapToSystems := watch.EnqueueChillSystems(r.Client, func(system edgev1alpha1.ChillSystem) bool {
		return system.Spec.DeviceDiscovery.Enabled
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("deviceclass-discovery").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&corev1.Node{}, mapToSystems).
		Watches(&corev1.ConfigMap{}, mapToSystems).
		Complete(r)
}
