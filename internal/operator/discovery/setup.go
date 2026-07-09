package discovery

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// SetupWithManager sets up node and catalog watches for device discovery.
func (r *DeviceDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapToSystems := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		systems := &edgev1alpha1.ChillSystemList{}
		if err := r.List(ctx, systems); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "list ChillSystems for DeviceClass discovery")
			return nil
		}
		requests := make([]reconcile.Request, 0, len(systems.Items))
		for i := range systems.Items {
			if !systems.Items[i].Spec.DeviceDiscovery.Enabled {
				continue
			}
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: systems.Items[i].Name}})
		}
		return requests
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("deviceclass-discovery").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&corev1.Node{}, mapToSystems).
		Watches(&corev1.ConfigMap{}, mapToSystems).
		Complete(r)
}
