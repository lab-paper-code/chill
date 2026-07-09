package nodediscoverycontroller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate node-discovery operator options: %w", err)
	}

	mapToSystem := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: r.systemKey()}}
	})
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("node-discovery").
		For(&corev1.ConfigMap{}, builder.WithPredicates(namedObjectPredicate(r.Options.Namespace, r.Options.ConfigMapName))).
		Owns(&appsv1.DaemonSet{}).
		Watches(&edgev1alpha1.ChillSystem{}, mapToSystem, builder.WithPredicates(namedObjectPredicate(r.Options.Namespace, r.Options.SystemName))).
		Complete(r); err != nil {
		return err
	}

	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if synced := mgr.GetCache().WaitForCacheSync(ctx); !synced {
			return fmt.Errorf("wait for cache sync")
		}
		log := ctrl.LoggerFrom(ctx).WithName("node-discovery")
		wait.UntilWithContext(ctx, func(ctx context.Context) {
			if _, err := r.reconcile(ctx); err != nil {
				log.Error(err, "refresh node-discovery")
			}
		}, r.Options.ReconcileInterval)
		return nil
	}))
}

func namedObjectPredicate(namespace, name string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isNamedObject(e.Object, namespace, name) },
		DeleteFunc: func(e event.DeleteEvent) bool { return isNamedObject(e.Object, namespace, name) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isNamedObject(e.ObjectNew, namespace, name) || isNamedObject(e.ObjectOld, namespace, name)
		},
		GenericFunc: func(e event.GenericEvent) bool { return isNamedObject(e.Object, namespace, name) },
	}
}

func isNamedObject(obj client.Object, namespace, name string) bool {
	if obj == nil {
		return false
	}
	if namespace != "" && obj.GetNamespace() != namespace {
		return false
	}
	return obj.GetName() == name
}
