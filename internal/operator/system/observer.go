package system

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *ChillSystemReconciler) observe(ctx context.Context, system *edgev1alpha1.ChillSystem) Observation {
	observed := Observation{
		ObservedGeneration:         system.Generation,
		Namespace:                  r.managementNamespace(system),
		NodeDiscoveryEnabled:       system.Spec.NodeDiscovery.Enabled,
		NodeDiscoveryDaemonSetName: nodeDiscoveryDaemonSetName(system),
		ProfilerEnabled:            system.Spec.Profiler.Enabled,
		ProfilerNamespace:          r.namespace(),
		ProfilerDeploymentName:     r.profilerDeploymentName(system),
	}

	if observed.NodeDiscoveryEnabled {
		daemonSet := &appsv1.DaemonSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: observed.Namespace, Name: observed.NodeDiscoveryDaemonSetName}, daemonSet); err != nil {
			if !apierrors.IsNotFound(err) {
				observed.NodeDiscoveryError = fmt.Errorf("observe node-discovery DaemonSet: %w", err)
			}
		} else {
			observed.NodeDiscoveryDaemonSet = daemonSet
		}
	}

	if observed.ProfilerEnabled {
		deployment := &appsv1.Deployment{}
		key := types.NamespacedName{Namespace: observed.ProfilerNamespace, Name: observed.ProfilerDeploymentName}
		if err := r.Get(ctx, key, deployment); err != nil {
			if !apierrors.IsNotFound(err) {
				observed.ProfilerError = fmt.Errorf("observe Profiler Deployment: %w", err)
			}
		} else {
			observed.ProfilerDeployment = deployment
		}
	}

	return observed
}
