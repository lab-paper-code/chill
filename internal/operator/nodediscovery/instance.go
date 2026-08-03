package nodediscovery

import (
	"cmp"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
)

func (r *Reconciler) runtimeOptions(system *edgev1alpha1.ChillSystem) Options {
	options := r.Options.Defaulted()
	options.SystemName = system.Name
	options.Namespace = cmp.Or(strings.TrimSpace(system.Spec.ManagementNamespace), options.Namespace)
	options.DaemonSetName = cmp.Or(
		strings.TrimSpace(system.Spec.NodeDiscovery.DaemonSetName),
		component.NodeDiscoveryDaemonSetName(system.Name),
	)
	options.ConfigMapName = cmp.Or(
		strings.TrimSpace(system.Spec.NodeDiscovery.ConfigMapName),
		component.NodeDiscoveryConfigMapName(system.Name),
	)
	options.ConfigMapKey = cmp.Or(strings.TrimSpace(system.Spec.NodeDiscovery.ConfigMapKey), DefaultConfigKey)
	return options
}
