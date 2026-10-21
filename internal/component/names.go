package component

import (
	"cmp"
	"strings"
)

const (
	DefaultSystemName = "chill"

	NodeAgent          = "node-agent"
	NodeAgentActuation = "node-agent-actuation"
	Profiler           = "profiler"
	EvidenceStore      = "evidence-store"

	EvidenceStoreConfigKey            = "config.yaml"
	EvidenceStoreS3Port         int32 = 9000
	EvidenceStoreAccessKeyKey         = "access-key"
	EvidenceStoreSecretKeyKey         = "secret-key"
	ProfilerEvidenceStagingPath       = "/var/run/chill/evidence"
)

func NodeAgentDaemonSetName(systemName string) string {
	return defaultSystemName(systemName) + "-node-agent"
}

func NodeAgentActuationDaemonSetName(systemName string) string {
	return defaultSystemName(systemName) + "-node-agent-actuation"
}

// LegacyNodeDaemonSetNames are the DaemonSets the node agent replaced:
// node discovery, then the node actuator. The operator deletes them, the
// actuator only once no retained transition depends on it.
func LegacyNodeDaemonSetNames(systemName string) (discovery, actuator string) {
	return defaultSystemName(systemName) + "-node-discovery", defaultSystemName(systemName) + "-node-actuator"
}

func NodeAgentConfigMapName(systemName string) string {
	return defaultSystemName(systemName) + "-node-agent-config"
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
	return cmp.Or(strings.TrimSpace(systemName), DefaultSystemName)
}
