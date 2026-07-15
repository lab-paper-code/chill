package evidencestore

import (
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/defaults"
)

// Instance is the resolved Kubernetes identity for one Evidence Store operand.
type Instance struct {
	SystemName                string
	Namespace                 string
	DeploymentName            string
	ServiceName               string
	ConfigMapName             string
	ConfigMapKey              string
	CredentialsSecretName     string
	PersistentVolumeClaimName string
	BootstrapJobName          string
}

func resolveInstance(system *edgev1alpha1.ChillSystem, base Options) Instance {
	options := base.Defaulted()
	return Instance{
		SystemName:     system.Name,
		Namespace:      defaults.String(strings.TrimSpace(system.Spec.ManagementNamespace), options.Namespace),
		DeploymentName: component.EvidenceStoreDeploymentName(system.Name),
		ServiceName:    component.EvidenceStoreServiceName(system.Name),
		ConfigMapName: defaults.String(
			strings.TrimSpace(system.Spec.EvidenceStore.ConfigRef.Name),
			component.EvidenceStoreConfigMapName(system.Name),
		),
		ConfigMapKey:              options.ConfigMapKey,
		CredentialsSecretName:     component.EvidenceStoreCredentialsSecretName(system.Name),
		PersistentVolumeClaimName: component.EvidenceStorePersistentVolumeClaimName(system.Name),
		BootstrapJobName:          component.EvidenceStoreBootstrapJobName(system.Name),
	}
}
