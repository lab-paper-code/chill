package evidencestore

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

type BootstrapState string

const (
	BootstrapStateWaiting   BootstrapState = "WaitingForDeployment"
	BootstrapStateRunning   BootstrapState = "Running"
	BootstrapStateSucceeded BootstrapState = "Succeeded"
	BootstrapStateFailed    BootstrapState = "Failed"
)

func reconcileBootstrapJob(
	ctx context.Context,
	k8sClient client.Client,
	scheme *runtime.Scheme,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
	config Config,
) (BootstrapState, bool, error) {
	desired, err := buildBootstrapJob(instance, config)
	if err != nil {
		return "", false, err
	}
	key := types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}
	current := &batchv1.Job{}
	err = k8sClient.Get(ctx, key, current)
	if err == nil {
		if err := requireControlledBySystem(current, system); err != nil {
			return "", false, err
		}
		if current.Annotations[bootstrapSpecHashAnnotation] == desired.Annotations[bootstrapSpecHashAnnotation] {
			if !current.DeletionTimestamp.IsZero() {
				return BootstrapStateRunning, true, nil
			}
			return observeBootstrapJob(current), false, nil
		}

		available, err := deploymentAvailable(ctx, k8sClient, system, instance)
		if err != nil {
			return "", false, err
		}
		if !available {
			return BootstrapStateWaiting, false, nil
		}
		if err := k8sClient.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
			return "", false, fmt.Errorf("delete stale Evidence Store bootstrap Job %s: %w", key.String(), err)
		}
		return BootstrapStateRunning, true, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", false, fmt.Errorf("get Evidence Store bootstrap Job %s: %w", key.String(), err)
	}

	available, err := deploymentAvailable(ctx, k8sClient, system, instance)
	if err != nil {
		return "", false, err
	}
	if !available {
		return BootstrapStateWaiting, false, nil
	}

	desired.TypeMeta = metav1.TypeMeta{APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job"}
	if err := controllerutil.SetControllerReference(system, desired, scheme); err != nil {
		return "", false, fmt.Errorf("set Evidence Store bootstrap Job owner: %w", err)
	}
	if err := k8sClient.Create(ctx, desired); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return BootstrapStateRunning, true, nil
		}
		return "", false, fmt.Errorf("create Evidence Store bootstrap Job %s: %w", key.String(), err)
	}
	return BootstrapStateRunning, false, nil
}

func deploymentAvailable(
	ctx context.Context,
	k8sClient client.Reader,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
) (bool, error) {
	key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.DeploymentName}
	deployment := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get Evidence Store Deployment availability %s: %w", key.String(), err)
	}
	if err := requireControlledBySystem(deployment, system); err != nil {
		return false, err
	}
	if !deployment.DeletionTimestamp.IsZero() || deployment.Status.ObservedGeneration < deployment.Generation ||
		deployment.Status.AvailableReplicas < 1 {
		return false, nil
	}
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			return true, nil
		}
	}
	return false, nil
}

func observeBootstrapJob(job *batchv1.Job) BootstrapState {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return BootstrapStateSucceeded
		}
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return BootstrapStateFailed
		}
	}
	return BootstrapStateRunning
}

func deleteOwnedBootstrapJob(
	ctx context.Context,
	k8sClient client.Client,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
) error {
	key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.BootstrapJobName}
	current := &batchv1.Job{}
	if err := k8sClient.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get Evidence Store bootstrap Job %s: %w", key.String(), err)
	}
	if !metav1.IsControlledBy(current, system) {
		return nil
	}
	if err := k8sClient.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Evidence Store bootstrap Job %s: %w", key.String(), err)
	}
	return nil
}
