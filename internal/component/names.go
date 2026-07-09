package component

import "strings"

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
	systemName = strings.TrimSpace(systemName)
	if systemName == "" {
		return DefaultSystemName
	}
	return systemName
}
