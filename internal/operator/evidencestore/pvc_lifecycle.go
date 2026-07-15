package evidencestore

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PersistentVolumeClaimOutcome string

const (
	PersistentVolumeClaimOutcomeCreated  PersistentVolumeClaimOutcome = "Created"
	PersistentVolumeClaimOutcomeExisting PersistentVolumeClaimOutcome = "Existing"
	PersistentVolumeClaimOutcomeExpanded PersistentVolumeClaimOutcome = "Expanded"
)

type persistentVolumeClaimClient interface {
	Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error
	Create(context.Context, client.Object, ...client.CreateOption) error
	Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error
}

func ensurePersistentVolumeClaim(
	ctx context.Context,
	k8sClient persistentVolumeClaimClient,
	instance Instance,
	config Config,
) (*corev1.PersistentVolumeClaim, PersistentVolumeClaimOutcome, error) {
	desired := buildPersistentVolumeClaim(instance, config)
	key := types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}
	current := &corev1.PersistentVolumeClaim{}
	if err := k8sClient.Get(ctx, key, current); err == nil {
		return reconcileRetainedPersistentVolumeClaim(ctx, k8sClient, current, desired, instance)
	} else if !apierrors.IsNotFound(err) {
		return nil, "", fmt.Errorf("get Evidence Store PersistentVolumeClaim %s: %w", key.String(), err)
	}

	if err := k8sClient.Create(ctx, desired); err == nil {
		return desired, PersistentVolumeClaimOutcomeCreated, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return nil, "", fmt.Errorf("create Evidence Store PersistentVolumeClaim %s: %w", key.String(), err)
	}

	current = &corev1.PersistentVolumeClaim{}
	if err := k8sClient.Get(ctx, key, current); err != nil {
		return nil, "", fmt.Errorf(
			"get concurrently created Evidence Store PersistentVolumeClaim %s: %w",
			key.String(),
			err,
		)
	}
	return reconcileRetainedPersistentVolumeClaim(ctx, k8sClient, current, desired, instance)
}

func reconcileRetainedPersistentVolumeClaim(
	ctx context.Context,
	k8sClient persistentVolumeClaimClient,
	current, desired *corev1.PersistentVolumeClaim,
	instance Instance,
) (*corev1.PersistentVolumeClaim, PersistentVolumeClaimOutcome, error) {
	if err := validateRetainedPersistentVolumeClaim(current, desired, instance); err != nil {
		return nil, "", err
	}

	currentSize := current.Spec.Resources.Requests[corev1.ResourceStorage]
	desiredSize := desired.Spec.Resources.Requests[corev1.ResourceStorage]
	switch desiredSize.Cmp(currentSize) {
	case -1:
		return nil, "", fmt.Errorf(
			"Evidence Store PersistentVolumeClaim %s/%s cannot shrink from %s to %s",
			current.Namespace,
			current.Name,
			currentSize.String(),
			desiredSize.String(),
		)
	case 0:
		return current, PersistentVolumeClaimOutcomeExisting, nil
	default:
		original := current.DeepCopy()
		if current.Spec.Resources.Requests == nil {
			current.Spec.Resources.Requests = corev1.ResourceList{}
		}
		current.Spec.Resources.Requests[corev1.ResourceStorage] = desiredSize.DeepCopy()
		if err := k8sClient.Patch(ctx, current, client.MergeFrom(original)); err != nil {
			return nil, "", fmt.Errorf(
				"expand Evidence Store PersistentVolumeClaim %s/%s from %s to %s: %w",
				current.Namespace,
				current.Name,
				currentSize.String(),
				desiredSize.String(),
				err,
			)
		}
		return current, PersistentVolumeClaimOutcomeExpanded, nil
	}
}

func validateRetainedPersistentVolumeClaim(
	current, desired *corev1.PersistentVolumeClaim,
	instance Instance,
) error {
	key := types.NamespacedName{Namespace: current.Namespace, Name: current.Name}
	for label, expected := range operandLabels(instance.SystemName) {
		if current.Labels[label] != expected {
			return fmt.Errorf("Evidence Store PersistentVolumeClaim %s is not managed by this ChillSystem", key.String())
		}
	}
	if len(current.OwnerReferences) != 0 {
		return fmt.Errorf("Evidence Store PersistentVolumeClaim %s must not have owner references", key.String())
	}
	if desired.Spec.StorageClassName != nil &&
		(current.Spec.StorageClassName == nil || *current.Spec.StorageClassName != *desired.Spec.StorageClassName) {
		return fmt.Errorf("Evidence Store PersistentVolumeClaim %s cannot change storageClassName", key.String())
	}
	if len(current.Spec.AccessModes) != 1 || current.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		return fmt.Errorf("Evidence Store PersistentVolumeClaim %s must use ReadWriteOnce", key.String())
	}
	if current.Spec.VolumeMode != nil && *current.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
		return fmt.Errorf("Evidence Store PersistentVolumeClaim %s must use Filesystem volume mode", key.String())
	}
	currentSize, present := current.Spec.Resources.Requests[corev1.ResourceStorage]
	if !present || currentSize.Sign() <= 0 {
		return fmt.Errorf("Evidence Store PersistentVolumeClaim %s has no positive storage request", key.String())
	}
	return nil
}
