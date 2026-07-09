package nodediscovery

import (
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/defaults"
)

func (r *Reconciler) runtimeOptions(system *edgev1alpha1.ChillSystem) Options {
	options := r.Options.Defaulted()
	options.SystemName = system.Name
	options.Namespace = defaults.String(strings.TrimSpace(system.Spec.ManagementNamespace), options.Namespace)
	options.DaemonSetName = defaults.String(
		strings.TrimSpace(system.Spec.NodeDiscovery.DaemonSetName),
		component.NodeDiscoveryDaemonSetName(system.Name),
	)
	options.ConfigMapName = defaults.String(
		strings.TrimSpace(system.Spec.NodeDiscovery.ConfigMapName),
		component.NodeDiscoveryConfigMapName(system.Name),
	)
	options.ConfigMapKey = defaults.String(strings.TrimSpace(system.Spec.NodeDiscovery.ConfigMapKey), DefaultConfigKey)
	return options
}
