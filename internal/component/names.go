package component

import "github.com/lab-paper-code/chill/internal/defaults"

const (
	DefaultSystemName = "chill"

	NodeDiscovery = "node-discovery"
	Profiler      = "profiler"
)

func NodeDiscoveryDaemonSetName(systemName string) string {
	return defaultSystemName(systemName) + "-node-discovery"
}

func NodeDiscoveryConfigMapName(systemName string) string {
	return defaultSystemName(systemName) + "-node-discovery-config"
}

func ProfilerDeploymentName(systemName string) string {
	return defaultSystemName(systemName) + "-profiler"
}

func ProfilerConfigMapName(systemName string) string {
	return defaultSystemName(systemName) + "-profiler-config"
}

func defaultSystemName(systemName string) string {
	return defaults.String(systemName, DefaultSystemName)
}
