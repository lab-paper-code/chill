package evidencestore

import (
	"context"
	"fmt"
	"reflect"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const evidenceStoreFieldOwner = "chill-operator-evidence-store"

func applyDeployment(
	ctx context.Context,
	k8sClient client.Client,
	scheme *runtime.Scheme,
	system *edgev1alpha1.ChillSystem,
	desired *appsv1.Deployment,
) (bool, error) {
	key := types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}
	current := &appsv1.Deployment{}
	err := k8sClient.Get(ctx, key, current)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("get Evidence Store Deployment %s: %w", key.String(), err)
	}
	if err == nil {
		if err := requireControlledBySystem(current, system); err != nil {
			return false, err
		}
		if !reflect.DeepEqual(current.Spec.Selector, desired.Spec.Selector) {
			if err := k8sClient.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("delete drifted Evidence Store Deployment %s: %w", key.String(), err)
			}
			return true, nil
		}
	}

	desired.TypeMeta = metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"}
	if err := controllerutil.SetControllerReference(system, desired, scheme); err != nil {
		return false, fmt.Errorf("set Evidence Store Deployment owner: %w", err)
	}
	if apierrors.IsNotFound(err) {
		if err := k8sClient.Create(ctx, desired); err != nil {
			return false, fmt.Errorf("create Evidence Store Deployment %s: %w", key.String(), err)
		}
		return false, nil
	}
	if err := k8sClient.Patch(
		ctx,
		desired,
		client.Apply,
		client.FieldOwner(evidenceStoreFieldOwner),
		client.ForceOwnership,
	); err != nil {
		return false, fmt.Errorf("apply Evidence Store Deployment %s: %w", key.String(), err)
	}
	return false, nil
}

func deleteOwnedDeployment(
	ctx context.Context,
	k8sClient client.Client,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
) error {
	key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.DeploymentName}
	current := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get Evidence Store Deployment %s: %w", key.String(), err)
	}
	if !metav1.IsControlledBy(current, system) {
		return nil
	}
	if err := k8sClient.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Evidence Store Deployment %s: %w", key.String(), err)
	}
	return nil
}
