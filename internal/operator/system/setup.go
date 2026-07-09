package system

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operator/watch"
)

// SetupWithManager sets up event-driven and periodic status refreshes.
func (r *ChillSystemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate ChillSystem status options: %w", err)
	}

	mapToSystem := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		systems := &edgev1alpha1.ChillSystemList{}
		if err := r.List(ctx, systems); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "list ChillSystems for status refresh")
			return nil
		}
		requests := make([]reconcile.Request, 0, len(systems.Items))
		for i := range systems.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: systems.Items[i].Name}})
		}
		return requests
	})
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("chillsystem-status").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&appsv1.Deployment{}, mapToSystem, builder.WithPredicates(watch.NamedObject(r.namespace(), r.operatorDeploymentName()))).
		Watches(&appsv1.DaemonSet{}, mapToSystem).
		Watches(&edgev1alpha1.DeviceClass{}, mapToSystem, builder.WithPredicates(watch.CreateDelete())).
		Watches(&corev1.Node{}, mapToSystem, builder.WithPredicates(watch.CreateDelete())).
		Complete(r); err != nil {
		return err
	}

	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if synced := mgr.GetCache().WaitForCacheSync(ctx); !synced {
			return fmt.Errorf("wait for cache sync")
		}
		wait.UntilWithContext(ctx, func(ctx context.Context) {
			systems := &edgev1alpha1.ChillSystemList{}
			if err := r.List(ctx, systems); err != nil {
				ctrl.LoggerFrom(ctx).Error(err, "list ChillSystems for periodic status refresh")
				return
			}
			for i := range systems.Items {
				if _, err := r.reconcileSystem(ctx, systems.Items[i].Name); err != nil {
					ctrl.LoggerFrom(ctx).Error(err, "refresh ChillSystem status", "chillsystem", systems.Items[i].Name)
				}
			}
		}, r.refreshInterval())
		return nil
	}))
}
