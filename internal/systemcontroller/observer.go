package systemcontroller

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
		Namespace:          r.namespace(),

		ControllerDeploymentName: r.controllerDeploymentName(),

		NodeDiscoveryEnabled:       r.Options.NodeDiscoveryEnabled,
		NodeDiscoveryDaemonSetName: r.nodeDiscoveryDaemonSetName(),
	}

	controller := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.namespace(), Name: r.controllerDeploymentName()}, controller); err != nil {
		if !apierrors.IsNotFound(err) {
			observed.ControllerError = fmt.Errorf("observe controller Deployment: %w", err)
		}
	} else {
		observed.ControllerDeployment = controller
	}

	if r.Options.NodeDiscoveryEnabled {
		daemonSet := &appsv1.DaemonSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: r.namespace(), Name: r.nodeDiscoveryDaemonSetName()}, daemonSet); err != nil {
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
