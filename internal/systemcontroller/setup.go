package systemcontroller

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

// SetupWithManager sets up event-driven and periodic status refreshes.
func (r *ChillSystemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate ChillSystem status options: %w", err)
	}

	mapToSystem := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: r.systemKey()}}
	})
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("chillsystem-status").
		For(&edgev1alpha1.ChillSystem{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&appsv1.Deployment{}, mapToSystem, builder.WithPredicates(namedObjectPredicate(r.namespace(), r.operatorDeploymentName()))).
		Watches(&appsv1.DaemonSet{}, mapToSystem, builder.WithPredicates(namedObjectPredicate(r.namespace(), r.nodeDiscoveryDaemonSetName()))).
		Watches(&edgev1alpha1.DeviceClass{}, mapToSystem, builder.WithPredicates(createDeletePredicate())).
		Watches(&corev1.Node{}, mapToSystem, builder.WithPredicates(createDeletePredicate())).
		Complete(r); err != nil {
		return err
	}

	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if synced := mgr.GetCache().WaitForCacheSync(ctx); !synced {
			return fmt.Errorf("wait for cache sync")
		}
		wait.UntilWithContext(ctx, func(ctx context.Context) {
			if _, err := r.reconcileSystem(ctx); err != nil {
				ctrl.LoggerFrom(ctx).Error(err, "refresh ChillSystem status")
			}
		}, r.refreshInterval())
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

func createDeletePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
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
