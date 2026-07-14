package profiler

import (
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/defaults"
)

func (r *Reconciler) runtimeOptions(system *edgev1alpha1.ChillSystem) Options {
	options := r.Options.Defaulted()
	options.SystemName = system.Name
	options.DeploymentName = component.ProfilerDeploymentName(system.Name)
	options.ConfigMapName = defaults.String(
		strings.TrimSpace(system.Spec.Profiler.ConfigRef.Name),
		component.ProfilerConfigMapName(system.Name),
	)
	return options
}
