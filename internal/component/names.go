package component

import "github.com/lab-paper-code/chill/internal/defaults"

const (
	DefaultSystemName = "chill"

	NodeDiscovery = "node-discovery"
	Profiler      = "profiler"
	EvidenceStore = "evidence-store"

	EvidenceStoreConfigKey          = "config.yaml"
	EvidenceStoreS3Port       int32 = 9000
	EvidenceStoreAccessKeyKey       = "access-key"
	EvidenceStoreSecretKeyKey       = "secret-key"
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

func ProfilerFactSourceConfigMapName(systemName string) string {
	return defaultSystemName(systemName) + "-profiler-facts"
}

func EvidenceStoreDeploymentName(systemName string) string {
	return defaultSystemName(systemName) + "-evidence-store"
}

func EvidenceStoreServiceName(systemName string) string {
	return defaultSystemName(systemName) + "-evidence-store"
}

func EvidenceStoreConfigMapName(systemName string) string {
	return defaultSystemName(systemName) + "-evidence-store-config"
}

func EvidenceStoreCredentialsSecretName(systemName string) string {
	return defaultSystemName(systemName) + "-evidence-store-credentials"
}

func EvidenceStorePersistentVolumeClaimName(systemName string) string {
	return defaultSystemName(systemName) + "-evidence-store"
}

func EvidenceStoreBootstrapJobName(systemName string) string {
	return defaultSystemName(systemName) + "-evidence-store-bootstrap"
}

func defaultSystemName(systemName string) string {
	return defaults.String(systemName, DefaultSystemName)
}
