package nodediscoverycontroller

import (
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// Config is the rendered node-discovery DaemonSet contract read by the operator.
type Config struct {
	Image                  string                         `json:"image"`
	ImagePullPolicy        corev1.PullPolicy              `json:"imagePullPolicy"`
	ServiceAccountName     string                         `json:"serviceAccountName"`
	HostRoot               string                         `json:"hostRoot"`
	Interval               string                         `json:"interval"`
	SignatureFile          string                         `json:"signatureFile"`
	SignatureConfigMapName string                         `json:"signatureConfigMapName"`
	SignatureConfigMapKey  string                         `json:"signatureConfigMapKey"`
	CleanupOnExit          bool                           `json:"cleanupOnExit"`
	CleanupTimeout         string                         `json:"cleanupTimeout"`
	KubeAPIServer          string                         `json:"kubeAPIServer,omitempty"`
	KubeAPITokenFile       string                         `json:"kubeAPITokenFile"`
	KubeAPICAFile          string                         `json:"kubeAPICAFile"`
	NodeSelector           map[string]string              `json:"nodeSelector,omitempty"`
	ExcludeNodeNames       []string                       `json:"excludeNodeNames,omitempty"`
	Affinity               *corev1.Affinity               `json:"affinity,omitempty"`
	Tolerations            []corev1.Toleration            `json:"tolerations,omitempty"`
	Resources              corev1.ResourceRequirements    `json:"resources,omitempty"`
	PodSecurityContext     *corev1.PodSecurityContext     `json:"podSecurityContext,omitempty"`
	SecurityContext        *corev1.SecurityContext        `json:"securityContext,omitempty"`
	HostPaths              []HostPathMount                `json:"hostPaths,omitempty"`
	UpdateStrategy         appsv1.DaemonSetUpdateStrategy `json:"updateStrategy"`
}

type HostPathMount struct {
	Name      string `json:"name"`
	HostPath  string `json:"hostPath"`
	MountPath string `json:"mountPath"`
}

func (c Config) Validate() error {
	var problems []string
	required := []struct {
		name  string
		value string
	}{
		{name: "image", value: c.Image},
		{name: "imagePullPolicy", value: string(c.ImagePullPolicy)},
		{name: "serviceAccountName", value: c.ServiceAccountName},
		{name: "hostRoot", value: c.HostRoot},
		{name: "interval", value: c.Interval},
		{name: "signatureFile", value: c.SignatureFile},
		{name: "signatureConfigMapName", value: c.SignatureConfigMapName},
		{name: "signatureConfigMapKey", value: c.SignatureConfigMapKey},
		{name: "kubeAPITokenFile", value: c.KubeAPITokenFile},
		{name: "kubeAPICAFile", value: c.KubeAPICAFile},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			problems = append(problems, field.name+" is required")
		}
	}
	if _, err := time.ParseDuration(c.Interval); c.Interval != "" && err != nil {
		problems = append(problems, fmt.Sprintf("interval must be a duration: %v", err))
	}
	if c.CleanupOnExit {
		if strings.TrimSpace(c.CleanupTimeout) == "" {
			problems = append(problems, "cleanupTimeout is required when cleanupOnExit=true")
		} else if _, err := time.ParseDuration(c.CleanupTimeout); err != nil {
			problems = append(problems, fmt.Sprintf("cleanupTimeout must be a duration: %v", err))
		}
	}
	if c.UpdateStrategy.Type == "" {
		problems = append(problems, "updateStrategy.type is required")
	}
	if len(c.HostPaths) == 0 {
		problems = append(problems, "hostPaths must contain at least one mount")
	}
	for i, hostPath := range c.HostPaths {
		if strings.TrimSpace(hostPath.Name) == "" {
			problems = append(problems, fmt.Sprintf("hostPaths[%d].name is required", i))
		}
		if strings.TrimSpace(hostPath.HostPath) == "" {
			problems = append(problems, fmt.Sprintf("hostPaths[%d].hostPath is required", i))
		}
		if strings.TrimSpace(hostPath.MountPath) == "" {
			problems = append(problems, fmt.Sprintf("hostPaths[%d].mountPath is required", i))
		}
	}
	if len(c.ExcludeNodeNames) > 0 && hasRequiredNodeAffinity(c.Affinity) {
		problems = append(problems, "excludeNodeNames cannot be combined with required nodeAffinity")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func hasRequiredNodeAffinity(affinity *corev1.Affinity) bool {
	return affinity != nil &&
		affinity.NodeAffinity != nil &&
		affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil
}
