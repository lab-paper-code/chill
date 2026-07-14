package profiler

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate Profiler operator options: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("profiler-operand").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.mapConfigMapToSystem)).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

func (r *Reconciler) mapConfigMapToSystem(ctx context.Context, object client.Object) []reconcile.Request {
	if object.GetNamespace() != r.Options.Namespace {
		return nil
	}
	system := &edgev1alpha1.ChillSystem{}
	key := types.NamespacedName{Name: r.Options.SystemName}
	if err := r.Get(ctx, key, system); err != nil {
		if !apierrors.IsNotFound(err) {
			ctrl.LoggerFrom(ctx).Error(err, "get ChillSystem for Profiler ConfigMap event", "name", key.Name)
		}
		return nil
	}
	if !system.Spec.Profiler.Enabled || object.GetName() != r.runtimeOptions(system).ConfigMapName {
		return nil
	}
	return []reconcile.Request{{NamespacedName: key}}
}
