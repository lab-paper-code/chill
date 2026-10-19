package evidencestore

import (
	"github.com/lab-paper-code/chill/internal/component"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const edgeMeshServiceProxyLabel = "service.edgemesh.kubeedge.io/service-proxy-name"

func operandLabels(systemName string) map[string]string {
	labels := selectorLabels(systemName)
	labels["app.kubernetes.io/managed-by"] = "chill-operator"
	labels["app.kubernetes.io/part-of"] = component.DefaultSystemName
	labels[profilingrun.MeasurementWorkloadLabelKey] = profilingrun.SystemRole
	return labels
}

func selectorLabels(systemName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      component.DefaultSystemName,
		"app.kubernetes.io/instance":  systemName,
		"app.kubernetes.io/component": component.EvidenceStore,
	}
}
