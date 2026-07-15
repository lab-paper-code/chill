package evidencestore

import "github.com/lab-paper-code/chill/internal/component"

const edgeMeshServiceProxyLabel = "service.edgemesh.kubeedge.io/service-proxy-name"

func operandLabels(systemName string) map[string]string {
	labels := selectorLabels(systemName)
	labels["app.kubernetes.io/managed-by"] = "chill-operator"
	labels["app.kubernetes.io/part-of"] = component.DefaultSystemName
	return labels
}

func selectorLabels(systemName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      component.DefaultSystemName,
		"app.kubernetes.io/instance":  systemName,
		"app.kubernetes.io/component": component.EvidenceStore,
	}
}
