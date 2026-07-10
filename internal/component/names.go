package component

import "github.com/lab-paper-code/chill/internal/defaults"

const (
	DefaultSystemName = "chill"

	NodeDiscovery = "node-discovery"
)

func NodeDiscoveryDaemonSetName(systemName string) string {
	return defaultSystemName(systemName) + "-node-discovery"
}

func NodeDiscoveryConfigMapName(systemName string) string {
	return defaultSystemName(systemName) + "-node-discovery-config"
}

func defaultSystemName(systemName string) string {
	return defaults.String(systemName, DefaultSystemName)
}
