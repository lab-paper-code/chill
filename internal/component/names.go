package component

import "github.com/lab-paper-code/chill/internal/defaults"

const (
	DefaultSystemName = "chill"

	Operator      = "operator"
	NodeDiscovery = "node-discovery"
)

func OperatorDeploymentName(systemName string) string {
	return defaultSystemName(systemName) + "-operator"
}

func NodeDiscoveryDaemonSetName(systemName string) string {
	return defaultSystemName(systemName) + "-node-discovery"
}

func NodeDiscoveryConfigMapName(systemName string) string {
	return defaultSystemName(systemName) + "-node-discovery-config"
}

func defaultSystemName(systemName string) string {
	return defaults.String(systemName, DefaultSystemName)
}
