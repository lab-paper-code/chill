package system

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *ChillSystemReconciler) observe(ctx context.Context, system *edgev1alpha1.ChillSystem) Observation {
	observed := Observation{
		ObservedGeneration: system.Generation,
		Namespace:          r.managementNamespace(system),

		OperatorNamespace:      r.namespace(),
		OperatorDeploymentName: r.operatorDeploymentName(),

		NodeDiscoveryEnabled:       system.Spec.NodeDiscovery.Enabled,
		NodeDiscoveryDaemonSetName: nodeDiscoveryDaemonSetName(system),
	}

	operator := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.namespace(), Name: r.operatorDeploymentName()}, operator); err != nil {
		if !apierrors.IsNotFound(err) {
			observed.OperatorError = fmt.Errorf("observe operator Deployment: %w", err)
		}
	} else {
		observed.OperatorDeployment = operator
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

	deviceClasses := &edgev1alpha1.DeviceClassList{}
	if err := r.List(ctx, deviceClasses); err != nil {
		observed.DeviceClassError = fmt.Errorf("observe DeviceClasses: %w", err)
	} else {
		observed.DeviceClassCount = int32Ptr(int32(len(deviceClasses.Items)))
	}

	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		observed.NodeError = fmt.Errorf("observe Nodes: %w", err)
	} else {
		observed.ObservedNodeCount = int32Ptr(int32(len(nodes.Items)))
	}

	return observed
}
