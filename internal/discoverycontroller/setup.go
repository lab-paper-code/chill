package discoverycontroller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SetupWithManager sets up node and catalog watches for device discovery.
func (r *DeviceDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapToSingleton := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "cluster"}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("deviceclass-discovery").
		Watches(&corev1.Node{}, mapToSingleton).
		Watches(&corev1.ConfigMap{}, mapToSingleton, builder.WithPredicates(catalogConfigMapPredicate(r.Options.CatalogNamespace, r.Options.CatalogName))).
		Complete(r)
}

func catalogConfigMapPredicate(namespace, name string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isCatalogObject(e.Object, namespace, name) },
		DeleteFunc: func(e event.DeleteEvent) bool { return isCatalogObject(e.Object, namespace, name) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isCatalogObject(e.ObjectNew, namespace, name) || isCatalogObject(e.ObjectOld, namespace, name)
		},
		GenericFunc: func(e event.GenericEvent) bool { return isCatalogObject(e.Object, namespace, name) },
	}
}

func isCatalogObject(obj client.Object, namespace, name string) bool {
	if name == "" {
		return false
	}
	if namespace != "" && obj.GetNamespace() != namespace {
		return false
	}
	return obj.GetName() == name
}
