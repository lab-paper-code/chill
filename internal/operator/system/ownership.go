package system

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *ChillSystemReconciler) ensureSystemOwnerReference(ctx context.Context, system *edgev1alpha1.ChillSystem) (bool, error) {
	changed, err := r.setSystemOwnerReference(ctx, system)
	if err != nil || !changed {
		return false, err
	}
	if err := r.Update(ctx, system); err != nil {
		return false, fmt.Errorf("update ChillSystem %s owner reference: %w", r.systemKey().String(), err)
	}
	return true, nil
}

func (r *ChillSystemReconciler) setSystemOwnerReference(ctx context.Context, system *edgev1alpha1.ChillSystem) (bool, error) {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: r.namespace(), Name: r.operatorDeploymentName()}
	if err := r.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get operator Deployment %s for ChillSystem owner reference: %w", key.String(), err)
	}
	if deployment.UID == "" {
		return false, nil
	}

	owner := metav1.OwnerReference{
		APIVersion: appsv1.SchemeGroupVersion.String(),
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
	}
	if hasOwnerReference(system.OwnerReferences, owner) {
		return false, nil
	}
	system.OwnerReferences = upsertOwnerReference(system.OwnerReferences, owner)
	return true, nil
}

func upsertOwnerReference(existing []metav1.OwnerReference, owner metav1.OwnerReference) []metav1.OwnerReference {
	next := append([]metav1.OwnerReference(nil), existing...)
	for i := range next {
		if next[i].APIVersion == owner.APIVersion && next[i].Kind == owner.Kind && next[i].Name == owner.Name {
			next[i] = owner
			return next
		}
	}
	return append(next, owner)
}

func hasOwnerReference(existing []metav1.OwnerReference, owner metav1.OwnerReference) bool {
	for _, current := range existing {
		if current.APIVersion == owner.APIVersion &&
			current.Kind == owner.Kind &&
			current.Name == owner.Name &&
			current.UID == owner.UID {
			return true
		}
	}
	return false
}
