package evidencestore

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// SetupWithManager registers the Evidence Store desired-state and operand watches.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate Evidence Store operator options: %w", err)
	}

	mapRetainedOrConfig := handler.EnqueueRequestsFromMapFunc(r.mapResourceToSystem)
	return ctrl.NewControllerManagedBy(mgr).
		Named("evidence-store-operand").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&corev1.ConfigMap{}, mapRetainedOrConfig).
		Watches(&corev1.Secret{}, mapRetainedOrConfig).
		Watches(&corev1.PersistentVolumeClaim{}, mapRetainedOrConfig).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

func (r *Reconciler) mapResourceToSystem(ctx context.Context, object client.Object) []reconcile.Request {
	system := &edgev1alpha1.ChillSystem{}
	key := types.NamespacedName{Name: r.Options.SystemName}
	if err := r.Get(ctx, key, system); err != nil {
		if !apierrors.IsNotFound(err) {
			ctrl.LoggerFrom(ctx).Error(err, "get ChillSystem for Evidence Store resource event", "name", key.Name)
		}
		return nil
	}
	if !system.Spec.EvidenceStore.Enabled {
		return nil
	}

	instance := resolveInstance(system, r.Options)
	if object.GetNamespace() != instance.Namespace {
		return nil
	}
	var expectedName string
	switch object.(type) {
	case *corev1.ConfigMap:
		expectedName = instance.ConfigMapName
	case *corev1.Secret:
		expectedName = instance.CredentialsSecretName
	case *corev1.PersistentVolumeClaim:
		expectedName = instance.PersistentVolumeClaimName
	default:
		return nil
	}
	if object.GetName() != expectedName {
		return nil
	}
	return []reconcile.Request{{NamespacedName: key}}
}
