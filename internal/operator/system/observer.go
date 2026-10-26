package system

import (
	"cmp"
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/operator/nodeagent"
	"sigs.k8s.io/yaml"
)

func (r *ChillSystemReconciler) observe(ctx context.Context, system *edgev1alpha1.ChillSystem) Observation {
	observed := Observation{
		ObservedGeneration: system.Generation,
		Namespace:          r.managementNamespace(system),
		NodeAgent: DaemonSetObservation{
			Enabled: system.Spec.NodeAgent.Enabled,
			Name:    component.NodeAgentDaemonSetName(system.Name),
		},
		NodeAgentActuation: DaemonSetObservation{
			Enabled: system.Spec.NodeAgent.Enabled && system.Spec.NodeAgent.Actuation,
			Name:    component.NodeAgentActuationDaemonSetName(system.Name),
		},
		ProfilerEnabled:               system.Spec.Profiler.Enabled,
		ProfilerNamespace:             r.namespace(),
		ProfilerDeploymentName:        r.profilerDeploymentName(system),
		EvidenceStoreEnabled:          system.Spec.EvidenceStore.Enabled,
		EvidenceStoreNamespace:        r.managementNamespace(system),
		EvidenceStoreDeploymentName:   r.evidenceStoreDeploymentName(system),
		EvidenceStoreBootstrapJobName: r.evidenceStoreBootstrapJobName(system),
	}
	if observed.NodeAgentActuation.Enabled {
		configMap := &corev1.ConfigMap{}
		name := cmp.Or(system.Spec.NodeAgent.ConfigRef.Name, component.NodeAgentConfigMapName(system.Name))
		err := r.Get(ctx, types.NamespacedName{Namespace: observed.Namespace, Name: name}, configMap)
		var config nodeagent.Config
		if err == nil {
			err = yaml.UnmarshalStrict([]byte(configMap.Data[nodeagent.DefaultConfigKey]), &config)
		}
		if err != nil || config.Actuation == nil {
			observed.NodeAgentActuation.Error = fmt.Errorf("observe actuation configuration: %v", err)
		} else {
			observed.NodeAgentActuation.Enabled = false
			for _, policy := range config.Actuation.Policies {
				switch policy.ActuatorKind {
				case nodeactuator.ActuatorKindJetsonNVPModel:
					observed.NodeAgentActuation.Enabled = true
				case nodeactuator.ActuatorKindNvidiaLockedGPUClock:
					observed.NodeAgentNvidia = DaemonSetObservation{Enabled: true,
						Name: component.NodeAgentActuationDaemonSetName(system.Name) + "-nvidia"}
				}
			}
		}
	}

	for _, daemonSet := range []*DaemonSetObservation{&observed.NodeAgent, &observed.NodeAgentActuation, &observed.NodeAgentNvidia} {
		if !daemonSet.Enabled {
			continue
		}
		current := &appsv1.DaemonSet{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: observed.Namespace, Name: daemonSet.Name}, current); err != nil {
			if !apierrors.IsNotFound(err) {
				daemonSet.Error = fmt.Errorf("observe DaemonSet %s: %w", daemonSet.Name, err)
			}
		} else {
			daemonSet.DaemonSet = current
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
