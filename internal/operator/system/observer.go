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

	return observed
}
