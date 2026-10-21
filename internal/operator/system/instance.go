package system

import (
	"cmp"
	"strings"

	"github.com/lab-paper-code/chill/internal/component"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func (r *ChillSystemReconciler) managementNamespace(system *edgev1alpha1.ChillSystem) string {
	return cmp.Or(strings.TrimSpace(system.Spec.ManagementNamespace), r.namespace())
}

func (r *ChillSystemReconciler) profilerDeploymentName(system *edgev1alpha1.ChillSystem) string {
	return component.ProfilerDeploymentName(system.Name)
}

func (r *ChillSystemReconciler) evidenceStoreDeploymentName(system *edgev1alpha1.ChillSystem) string {
	return component.EvidenceStoreDeploymentName(system.Name)
}

func (r *ChillSystemReconciler) evidenceStoreBootstrapJobName(system *edgev1alpha1.ChillSystem) string {
	return component.EvidenceStoreBootstrapJobName(system.Name)
}
