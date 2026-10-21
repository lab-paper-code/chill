package nodeagent

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
	options.ConfigMapName = cmp.Or(
		strings.TrimSpace(system.Spec.NodeAgent.ConfigRef.Name),
		component.NodeAgentConfigMapName(system.Name),
	)
	return options
}
