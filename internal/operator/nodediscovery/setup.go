package nodediscovery

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate node-discovery operator options: %w", err)
	}

	mapToSystem := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		systems := &edgev1alpha1.ChillSystemList{}
		if err := r.List(ctx, systems); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "list ChillSystems for node-discovery")
			return nil
		}
		requests := make([]reconcile.Request, 0, len(systems.Items))
		for i := range systems.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: systems.Items[i].Name}})
		}
		return requests
	})
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("node-discovery").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&corev1.ConfigMap{}, mapToSystem).
		Owns(&appsv1.DaemonSet{}).
		Complete(r); err != nil {
		return err
	}

	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if synced := mgr.GetCache().WaitForCacheSync(ctx); !synced {
			return fmt.Errorf("wait for cache sync")
		}
		log := ctrl.LoggerFrom(ctx).WithName("node-discovery")
		wait.UntilWithContext(ctx, func(ctx context.Context) {
			systems := &edgev1alpha1.ChillSystemList{}
			if err := r.List(ctx, systems); err != nil {
				log.Error(err, "list ChillSystems for periodic node-discovery")
				return
			}
			for i := range systems.Items {
				if _, err := r.reconcile(ctx, systems.Items[i].Name); err != nil {
					log.Error(err, "refresh node-discovery", "chillsystem", systems.Items[i].Name)
				}
			}
		}, r.Options.ReconcileInterval)
		return nil
	}))
}
