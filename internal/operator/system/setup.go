package system

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operator/watch"
)

// SetupWithManager sets up event-driven and periodic status refreshes.
func (r *ChillSystemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate ChillSystem options: %w", err)
	}

	mapToSystem := watch.EnqueueChillSystems(r.Client, nil)
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("chillsystem").
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
