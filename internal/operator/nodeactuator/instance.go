package nodeactuator

import (
	"cmp"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
)

func runtimeOptions(system *edgev1alpha1.ChillSystem, base Options) Options {
	options := base.Defaulted()
	options.SystemName = system.Name
	options.Namespace = cmp.Or(strings.TrimSpace(system.Spec.ManagementNamespace), options.Namespace)
	options.DaemonSetName = component.NodeActuatorDaemonSetName(system.Name)
	options.ConfigMapName = cmp.Or(
		strings.TrimSpace(system.Spec.NodeActuator.ConfigRef.Name),
		component.NodeActuatorConfigMapName(system.Name),
	)
	return options
}
