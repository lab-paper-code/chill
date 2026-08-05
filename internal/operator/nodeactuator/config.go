package nodeactuator

import (
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
)

type Config struct {
	Image                string                             `json:"image"`
	ImagePullPolicy      corev1.PullPolicy                  `json:"imagePullPolicy"`
	ServiceAccountName   string                             `json:"serviceAccountName"`
	MetricsBindAddress   string                             `json:"metricsBindAddress"`
	HealthProbePort      int32                              `json:"healthProbePort"`
	LeaseDurationSeconds int32                              `json:"leaseDurationSeconds"`
	SafetyValidFor       string                             `json:"safetyValidFor"`
	AllowedNamespaces    []string                           `json:"allowedNamespaces"`
	ActuationPolicies    []actuatorcore.ActuationPolicySpec `json:"actuationPolicies"`
	NodeSelector         map[string]string                  `json:"nodeSelector"`
	Affinity             *corev1.Affinity                   `json:"affinity,omitempty"`
	Tolerations          []corev1.Toleration                `json:"tolerations,omitempty"`
	Resources            corev1.ResourceRequirements        `json:"resources,omitempty"`
	PodSecurityContext   *corev1.PodSecurityContext         `json:"podSecurityContext,omitempty"`
	SecurityContext      *corev1.SecurityContext            `json:"securityContext,omitempty"`
}

func (c Config) Validate() error {
	var problems []string
	for name, value := range map[string]string{
		"image": c.Image, "imagePullPolicy": string(c.ImagePullPolicy),
		"serviceAccountName": c.ServiceAccountName, "metricsBindAddress": c.MetricsBindAddress,
		"safetyValidFor": c.SafetyValidFor,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			problems = append(problems, name+" is required without surrounding whitespace")
		}
	}
	if c.HealthProbePort < 1 || c.HealthProbePort > 65535 {
		problems = append(problems, "healthProbePort must be between 1 and 65535")
	}
	if c.LeaseDurationSeconds < 5 || c.LeaseDurationSeconds > 300 {
		problems = append(problems, "leaseDurationSeconds must be between 5 and 300")
	}
	if duration, err := time.ParseDuration(c.SafetyValidFor); err != nil || duration < time.Second || duration > 5*time.Minute {
		problems = append(problems, "safetyValidFor must be a duration between 1 second and 5 minutes")
	}
	if len(c.NodeSelector) == 0 {
		problems = append(problems, "nodeSelector must explicitly select eligible Nodes")
	}
	if err := validateAllowedNamespaces(c.AllowedNamespaces); err != nil {
		problems = append(problems, err.Error())
	}
	if _, err := configuredPolicyDigests(c.ActuationPolicies); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) != 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateAllowedNamespaces(namespaces []string) error {
	if len(namespaces) == 0 {
		return errors.New("allowedNamespaces must contain at least one explicit namespace")
	}
	seen := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		if strings.TrimSpace(namespace) != namespace || len(validation.IsDNS1123Label(namespace)) != 0 {
			return fmt.Errorf("invalid allowed namespace %q", namespace)
		}
		if _, duplicate := seen[namespace]; duplicate {
			return fmt.Errorf("duplicate allowed namespace %q", namespace)
		}
		seen[namespace] = struct{}{}
	}
	return nil
}

func configuredPolicyDigests(specs []actuatorcore.ActuationPolicySpec) ([]string, error) {
	if len(specs) == 0 {
		return nil, errors.New("actuationPolicies must contain at least one policy")
	}
	seenNames := make(map[string]struct{}, len(specs))
	seenSelectors := make(map[string]struct{}, len(specs))
	digests := make([]string, 0, len(specs))
	for _, spec := range specs {
		policy := actuatorcore.ActuationPolicy{
			SchemaVersion:       actuatorcore.ActuationPolicySchemaVersion,
			ActuationPolicySpec: spec,
		}
		digest, err := policy.Digest()
		if err != nil {
			return nil, fmt.Errorf("validate configured actuation policy %q: %w", spec.Name, err)
		}
		if _, duplicate := seenNames[policy.Name]; duplicate {
			return nil, fmt.Errorf("duplicate configured actuation-policy name %q", policy.Name)
		}
		seenNames[policy.Name] = struct{}{}
		selector := policy.DeviceClass + "\x00" + policy.PowerMode + "\x00" + policy.ActuatorKind
		if _, duplicate := seenSelectors[selector]; duplicate {
			return nil, fmt.Errorf("duplicate configured actuation-policy selector %q", selector)
		}
		seenSelectors[selector] = struct{}{}
		digests = append(digests, digest)
	}
	return digests, nil
}
