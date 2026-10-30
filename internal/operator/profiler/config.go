package profiler

import (
	"errors"
	"math"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Config is the rendered Profiler Deployment contract read by the operator.
type Config struct {
	Replicas                  int32                       `json:"replicas"`
	Image                     string                      `json:"image"`
	ImagePullPolicy           corev1.PullPolicy           `json:"imagePullPolicy"`
	ServiceAccountName        string                      `json:"serviceAccountName"`
	MetricsBindAddress        string                      `json:"metricsBindAddress"`
	HealthProbePort           int32                       `json:"healthProbePort"`
	LeaderElection            bool                        `json:"leaderElection"`
	LeaderElectionID          string                      `json:"leaderElectionID"`
	FailedJobRetentionSeconds int32                       `json:"failedJobRetentionSeconds"`
	NodeSelector              map[string]string           `json:"nodeSelector,omitempty"`
	Affinity                  *corev1.Affinity            `json:"affinity,omitempty"`
	Tolerations               []corev1.Toleration         `json:"tolerations,omitempty"`
	Resources                 corev1.ResourceRequirements `json:"resources,omitempty"`
	PodSecurityContext        *corev1.PodSecurityContext  `json:"podSecurityContext,omitempty"`
	SecurityContext           *corev1.SecurityContext     `json:"securityContext,omitempty"`
	FactSource                FactSourceConfig            `json:"factSource"`
	WorkloadObserver          WorkloadObserverConfig      `json:"workloadObserver"`
	TransitionObserver        TransitionObserverConfig    `json:"transitionObserver"`
}

type FactSourceConfig struct {
	ConfigMapName          string `json:"configMapName"`
	ExecutionFactsKey      string `json:"executionFactsKey"`
	MetricSourcesKey       string `json:"metricSourcesKey"`
	WorkloadObservationKey string `json:"workloadObservationKey"`
	ExecutionPoliciesKey   string `json:"executionPoliciesKey"`
	ActuationPoliciesKey   string `json:"actuationPoliciesKey"`
}

type WorkloadObserverConfig struct {
	ServiceAccountName string `json:"serviceAccountName"`
	KubeAPIServer      string `json:"kubeAPIServer,omitempty"`
}

type TransitionObserverConfig struct {
	ObserverNodeName              string `json:"observerNodeName"`
	ServiceAccountName            string `json:"serviceAccountName"`
	ActiveDeadlineOverheadSeconds int64  `json:"activeDeadlineOverheadSeconds"`
	MaximumObjectBytes            int64  `json:"maximumObjectBytes"`
}

func (c Config) Validate() error {
	var problems []string
	if c.FailedJobRetentionSeconds < 1 || c.FailedJobRetentionSeconds > 604800 {
		problems = append(problems, "failedJobRetentionSeconds must be between 1 and 604800")
	}
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
		{name: "factSource.executionPoliciesKey", value: c.FactSource.ExecutionPoliciesKey},
		{name: "factSource.actuationPoliciesKey", value: c.FactSource.ActuationPoliciesKey},
		{name: "workloadObserver.serviceAccountName", value: c.WorkloadObserver.ServiceAccountName},
		{name: "transitionObserver.observerNodeName", value: c.TransitionObserver.ObserverNodeName},
		{name: "transitionObserver.serviceAccountName", value: c.TransitionObserver.ServiceAccountName},
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
		c.FactSource.ExecutionPoliciesKey,
		c.FactSource.ActuationPoliciesKey,
	} {
		keys[key] = struct{}{}
	}
	if len(keys) != 5 {
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
	if c.TransitionObserver.ActiveDeadlineOverheadSeconds < 1 ||
		c.TransitionObserver.ActiveDeadlineOverheadSeconds > math.MaxInt64/int64(time.Second) {
		problems = append(problems, "transitionObserver.activeDeadlineOverheadSeconds is invalid")
	}
	if c.TransitionObserver.MaximumObjectBytes < 1 ||
		c.TransitionObserver.MaximumObjectBytes == math.MaxInt64 {
		problems = append(problems, "transitionObserver.maximumObjectBytes is invalid")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
