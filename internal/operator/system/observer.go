package system

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *ChillSystemReconciler) observe(ctx context.Context, system *edgev1alpha1.ChillSystem) Observation {
	observed := Observation{
		ObservedGeneration:            system.Generation,
		Namespace:                     r.managementNamespace(system),
		NodeDiscoveryEnabled:          system.Spec.NodeDiscovery.Enabled,
		NodeDiscoveryDaemonSetName:    nodeDiscoveryDaemonSetName(system),
		ProfilerEnabled:               system.Spec.Profiler.Enabled,
		ProfilerNamespace:             r.namespace(),
		ProfilerDeploymentName:        r.profilerDeploymentName(system),
		NodeActuatorEnabled:           system.Spec.NodeActuator.Enabled,
		NodeActuatorNamespace:         r.managementNamespace(system),
		NodeActuatorDaemonSetName:     r.nodeActuatorDaemonSetName(system),
		EvidenceStoreEnabled:          system.Spec.EvidenceStore.Enabled,
		EvidenceStoreNamespace:        r.managementNamespace(system),
		EvidenceStoreDeploymentName:   r.evidenceStoreDeploymentName(system),
		EvidenceStoreBootstrapJobName: r.evidenceStoreBootstrapJobName(system),
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

	if observed.NodeActuatorEnabled {
		daemonSet := &appsv1.DaemonSet{}
		key := types.NamespacedName{Namespace: observed.NodeActuatorNamespace, Name: observed.NodeActuatorDaemonSetName}
		if err := r.Get(ctx, key, daemonSet); err != nil {
			if !apierrors.IsNotFound(err) {
				observed.NodeActuatorError = fmt.Errorf("observe NodeActuator DaemonSet: %w", err)
			}
		} else {
			observed.NodeActuatorDaemonSet = daemonSet
		}
	}

	if observed.EvidenceStoreEnabled {
		deployment := &appsv1.Deployment{}
		key := types.NamespacedName{Namespace: observed.EvidenceStoreNamespace, Name: observed.EvidenceStoreDeploymentName}
		if err := r.Get(ctx, key, deployment); err != nil {
			if !apierrors.IsNotFound(err) {
				observed.EvidenceStoreError = fmt.Errorf("observe Evidence Store Deployment: %w", err)
			}
		} else if metav1.IsControlledBy(deployment, system) {
			observed.EvidenceStoreDeployment = deployment
		}

		bootstrapJob := &batchv1.Job{}
		key = types.NamespacedName{Namespace: observed.EvidenceStoreNamespace, Name: observed.EvidenceStoreBootstrapJobName}
		if err := r.Get(ctx, key, bootstrapJob); err != nil {
			if !apierrors.IsNotFound(err) && observed.EvidenceStoreError == nil {
				observed.EvidenceStoreError = fmt.Errorf("observe Evidence Store bootstrap Job: %w", err)
			}
		} else if metav1.IsControlledBy(bootstrapJob, system) {
			observed.EvidenceStoreBootstrapJob = bootstrapJob
		}
	}

	return observed
}
