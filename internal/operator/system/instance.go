package system

import (
	"strings"

	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/defaults"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *ChillSystemReconciler) managementNamespace(system *edgev1alpha1.ChillSystem) string {
	return defaults.String(strings.TrimSpace(system.Spec.ManagementNamespace), r.namespace())
}

func nodeDiscoveryDaemonSetName(system *edgev1alpha1.ChillSystem) string {
	return defaults.String(
		strings.TrimSpace(system.Spec.NodeDiscovery.DaemonSetName),
		component.NodeDiscoveryDaemonSetName(system.Name),
	)
}
