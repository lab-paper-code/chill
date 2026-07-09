package discovery

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/lab-paper-code/chill/internal/operator/watch"
)

// SetupWithManager sets up node and catalog watches for device discovery.
func (r *DeviceDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapToSingleton := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "cluster"}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("deviceclass-discovery").
		Watches(&corev1.Node{}, mapToSingleton).
		Watches(&corev1.ConfigMap{}, mapToSingleton, builder.WithPredicates(watch.NamedObject(r.Options.CatalogNamespace, r.Options.CatalogName))).
		Complete(r)
}
