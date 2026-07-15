package evidencestore

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func preflightServiceOwnership(
	ctx context.Context,
	k8sClient client.Client,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
) error {
	key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.ServiceName}
	current := &corev1.Service{}
	if err := k8sClient.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get Evidence Store Service %s: %w", key.String(), err)
	}
	return requireControlledBySystem(current, system)
}

func applyService(
	ctx context.Context,
	k8sClient client.Client,
	scheme *runtime.Scheme,
	system *edgev1alpha1.ChillSystem,
	desired *corev1.Service,
) error {
	key := types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}
	current := &corev1.Service{}
	err := k8sClient.Get(ctx, key, current)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get Evidence Store Service %s: %w", key.String(), err)
	}
	if err == nil {
		if err := requireControlledBySystem(current, system); err != nil {
			return err
		}
	}

	desired.TypeMeta = metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Service"}
	if err := controllerutil.SetControllerReference(system, desired, scheme); err != nil {
		return fmt.Errorf("set Evidence Store Service owner: %w", err)
	}
	if apierrors.IsNotFound(err) {
		if err := k8sClient.Create(ctx, desired); err != nil {
			return fmt.Errorf("create Evidence Store Service %s: %w", key.String(), err)
		}
		return nil
	}
	if err := k8sClient.Patch(
		ctx,
		desired,
		client.Apply,
		client.FieldOwner(evidenceStoreFieldOwner),
		client.ForceOwnership,
	); err != nil {
		return fmt.Errorf("apply Evidence Store Service %s: %w", key.String(), err)
	}
	return nil
}

func deleteOwnedService(
	ctx context.Context,
	k8sClient client.Client,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
) error {
	key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.ServiceName}
	current := &corev1.Service{}
	if err := k8sClient.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get Evidence Store Service %s: %w", key.String(), err)
	}
	if !metav1.IsControlledBy(current, system) {
		return nil
	}
	if err := k8sClient.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Evidence Store Service %s: %w", key.String(), err)
	}
	return nil
}
