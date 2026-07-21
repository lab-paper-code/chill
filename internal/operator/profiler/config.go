package profiler

import (
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Config is the rendered Profiler Deployment contract read by the operator.
type Config struct {
	Replicas           int32                       `json:"replicas"`
	Image              string                      `json:"image"`
	ImagePullPolicy    corev1.PullPolicy           `json:"imagePullPolicy"`
	ServiceAccountName string                      `json:"serviceAccountName"`
	MetricsBindAddress string                      `json:"metricsBindAddress"`
	HealthProbePort    int32                       `json:"healthProbePort"`
	LeaderElection     bool                        `json:"leaderElection"`
	LeaderElectionID   string                      `json:"leaderElectionID"`
	NodeSelector       map[string]string           `json:"nodeSelector,omitempty"`
	Affinity           *corev1.Affinity            `json:"affinity,omitempty"`
	Tolerations        []corev1.Toleration         `json:"tolerations,omitempty"`
	Resources          corev1.ResourceRequirements `json:"resources,omitempty"`
	PodSecurityContext *corev1.PodSecurityContext  `json:"podSecurityContext,omitempty"`
	SecurityContext    *corev1.SecurityContext     `json:"securityContext,omitempty"`
	FactSource         FactSourceConfig            `json:"factSource"`
	WorkloadObserver   WorkloadObserverConfig      `json:"workloadObserver"`
}

type FactSourceConfig struct {
	ConfigMapName          string `json:"configMapName"`
	ExecutionFactsKey      string `json:"executionFactsKey"`
	MetricSourcesKey       string `json:"metricSourcesKey"`
	WorkloadObservationKey string `json:"workloadObservationKey"`
}

type WorkloadObserverConfig struct {
	ServiceAccountName string `json:"serviceAccountName"`
	KubeAPIServer      string `json:"kubeAPIServer,omitempty"`
}

func (c Config) Validate() error {
	var problems []string
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "image", value: c.Image},
		{name: "imagePullPolicy", value: string(c.ImagePullPolicy)},
		{name: "serviceAccountName", value: c.ServiceAccountName},
		{name: "metricsBindAddress", value: c.MetricsBindAddress},
		{name: "factSource.configMapName", value: c.FactSource.ConfigMapName},
		{name: "factSource.executionFactsKey", value: c.FactSource.ExecutionFactsKey},
		{name: "factSource.metricSourcesKey", value: c.FactSource.MetricSourcesKey},
		{name: "factSource.workloadObservationKey", value: c.FactSource.WorkloadObservationKey},
		{name: "workloadObserver.serviceAccountName", value: c.WorkloadObserver.ServiceAccountName},
	} {
		if strings.TrimSpace(field.value) == "" {
			problems = append(problems, field.name+" is required")
		}
	}
	if strings.TrimSpace(c.WorkloadObserver.KubeAPIServer) != c.WorkloadObserver.KubeAPIServer {
		problems = append(problems, "workloadObserver.kubeAPIServer must not have surrounding whitespace")
	}
	keys := map[string]struct{}{}
	for _, key := range []string{
		c.FactSource.ExecutionFactsKey,
		c.FactSource.MetricSourcesKey,
		c.FactSource.WorkloadObservationKey,
	} {
		keys[key] = struct{}{}
	}
	if len(keys) != 3 {
		problems = append(problems, "factSource keys must be different")
	}
	if c.Replicas < 1 {
		problems = append(problems, "replicas must be positive")
	}
	if strings.TrimSpace(string(c.ImagePullPolicy)) != "" {
		switch c.ImagePullPolicy {
		case corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
		default:
			problems = append(problems, "imagePullPolicy must be Always, IfNotPresent, or Never")
		}
	}
	if c.HealthProbePort < 1 || c.HealthProbePort > 65535 {
		problems = append(problems, "healthProbePort must be between 1 and 65535")
	}
	if c.LeaderElection && strings.TrimSpace(c.LeaderElectionID) == "" {
		problems = append(problems, "leaderElectionID is required when leaderElection is enabled")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func healthProbeAddress(port int32) string {
	return fmt.Sprintf(":%d", port)
}
