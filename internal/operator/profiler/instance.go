package profiler

import (
	"cmp"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
)

func runtimeOptions(system *edgev1alpha1.ChillSystem, base Options) Options {
	options := base.Defaulted()
	options.SystemName = system.Name
	options.DeploymentName = component.ProfilerDeploymentName(system.Name)
	options.ConfigMapName = cmp.Or(
		strings.TrimSpace(system.Spec.Profiler.ConfigRef.Name),
		component.ProfilerConfigMapName(system.Name),
	)
	return options
}
