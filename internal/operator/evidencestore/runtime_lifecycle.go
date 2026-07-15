package evidencestore

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func applyRuntimeResources(
	ctx context.Context,
	k8sClient client.Client,
	scheme *runtime.Scheme,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
	config Config,
) (bool, error) {
	if err := preflightServiceOwnership(ctx, k8sClient, system, instance); err != nil {
		return false, fmt.Errorf("reconcile Evidence Store runtime: %w", err)
	}
	requeue, err := applyDeployment(ctx, k8sClient, scheme, system, buildDeployment(instance, config))
	if err != nil {
		return false, fmt.Errorf("reconcile Evidence Store runtime: %w", err)
	}
	if requeue {
		return true, nil
	}
	if err := applyService(ctx, k8sClient, scheme, system, buildService(instance)); err != nil {
		return false, fmt.Errorf("reconcile Evidence Store runtime: %w", err)
	}
	return false, nil
}

func deleteRuntimeResources(
	ctx context.Context,
	k8sClient client.Client,
	system *edgev1alpha1.ChillSystem,
	instance Instance,
) error {
	return errors.Join(
		deleteOwnedDeployment(ctx, k8sClient, system, instance),
		deleteOwnedService(ctx, k8sClient, system, instance),
	)
}
