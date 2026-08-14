package nodeactuator

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/nodeactuator/jetson"
)

// rebootCapability is the one capability this operand may hold. A power mode
// the device establishes at boot is reached by restarting the Node, and this is
// the narrowest privilege that permits it. Nothing else is allowed alongside it.
const rebootCapability = corev1.Capability("SYS_BOOT")

type Config struct {
	Image                string                             `json:"image"`
	ImagePullPolicy      corev1.PullPolicy                  `json:"imagePullPolicy"`
	ServiceAccountName   string                             `json:"serviceAccountName"`
	MetricsBindAddress   string                             `json:"metricsBindAddress"`
	HealthProbePort      int32                              `json:"healthProbePort"`
	LeaseDurationSeconds int32                              `json:"leaseDurationSeconds"`
	KubeAPIServer        string                             `json:"kubeAPIServer,omitempty"`
	KubeAPITokenFile     string                             `json:"kubeAPITokenFile"`
	KubeAPICAFile        string                             `json:"kubeAPICAFile"`
	AllowedNamespaces    []string                           `json:"allowedNamespaces"`
	ActuationPolicies    []actuatorcore.ActuationPolicySpec `json:"actuationPolicies"`
	JetsonNVPModel       *JetsonNVPModelConfig              `json:"jetsonNVPModel,omitempty"`
	NodeSelector         map[string]string                  `json:"nodeSelector"`
	Affinity             *corev1.Affinity                   `json:"affinity,omitempty"`
	Tolerations          []corev1.Toleration                `json:"tolerations,omitempty"`
	Resources            corev1.ResourceRequirements        `json:"resources,omitempty"`
	PodSecurityContext   *corev1.PodSecurityContext         `json:"podSecurityContext,omitempty"`
	SecurityContext      *corev1.SecurityContext            `json:"securityContext,omitempty"`
}

type JetsonNVPModelConfig struct {
	Command        []string        `json:"command"`
	CommandTimeout string          `json:"commandTimeout"`
	HostPaths      []HostPathMount `json:"hostPaths"`
}

type HostPathMount struct {
	Name      string              `json:"name"`
	HostPath  string              `json:"hostPath"`
	MountPath string              `json:"mountPath"`
	Type      corev1.HostPathType `json:"type"`
	ReadOnly  bool                `json:"readOnly"`
}

func (c Config) Validate() error {
	var problems []string
	for name, value := range map[string]string{
		"image": c.Image, "imagePullPolicy": string(c.ImagePullPolicy),
		"serviceAccountName": c.ServiceAccountName, "metricsBindAddress": c.MetricsBindAddress,
		"kubeAPITokenFile": c.KubeAPITokenFile,
		"kubeAPICAFile":    c.KubeAPICAFile,
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
	if strings.TrimSpace(c.KubeAPIServer) != c.KubeAPIServer {
		problems = append(problems, "kubeAPIServer must not have surrounding whitespace")
	}
	if len(c.NodeSelector) == 0 {
		problems = append(problems, "nodeSelector must explicitly select eligible Nodes")
	}
	if err := validateAllowedNamespaces(c.AllowedNamespaces); err != nil {
		problems = append(problems, err.Error())
	}
	if _, err := configuredPolicies(c.ActuationPolicies); err != nil {
		problems = append(problems, err.Error())
	}
	if err := c.validateHostAdapters(); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) != 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (c Config) validateHostAdapters() error {
	for _, policy := range c.ActuationPolicies {
		if policy.ActuatorKind != actuatorcore.ActuatorKindJetsonNVPModel {
			return fmt.Errorf("no installed host adapter supports actuation-policy kind %q", policy.ActuatorKind)
		}
	}
	if c.JetsonNVPModel == nil {
		return errors.New("jetsonNVPModel config is required by the installed actuation policies")
	}
	duration, err := time.ParseDuration(c.JetsonNVPModel.CommandTimeout)
	if err != nil {
		return errors.New("jetsonNVPModel.commandTimeout must be a duration")
	}
	if err := (jetson.NVPModelOptions{Command: c.JetsonNVPModel.Command, Timeout: duration}).Validate(); err != nil {
		return fmt.Errorf("validate jetsonNVPModel command: %w", err)
	}
	if err := validateHostPaths(c.JetsonNVPModel.HostPaths); err != nil {
		return fmt.Errorf("validate jetsonNVPModel host paths: %w", err)
	}
	if c.PodSecurityContext == nil || c.PodSecurityContext.RunAsUser == nil || *c.PodSecurityContext.RunAsUser != 0 ||
		c.PodSecurityContext.RunAsNonRoot == nil || *c.PodSecurityContext.RunAsNonRoot {
		return errors.New("jetsonNVPModel requires an explicit root Pod security identity")
	}
	security := c.SecurityContext
	if security == nil || security.Privileged == nil || *security.Privileged ||
		security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem || security.Capabilities == nil ||
		len(security.Capabilities.Add) != 1 || security.Capabilities.Add[0] != rebootCapability ||
		!slices.Contains(security.Capabilities.Drop, corev1.Capability("ALL")) {
		return errors.New("jetsonNVPModel requires non-privileged, no-escalation, read-only-rootfs security dropping all capabilities and adding only SYS_BOOT")
	}
	return nil
}

func validateHostPaths(hostPaths []HostPathMount) error {
	if len(hostPaths) == 0 {
		return errors.New("at least one explicit host path is required")
	}
	seenNames := make(map[string]struct{}, len(hostPaths))
	seenHostPaths := make(map[string]struct{}, len(hostPaths))
	seenMountPaths := make(map[string]struct{}, len(hostPaths))
	for _, hostPath := range hostPaths {
		if len(validation.IsDNS1123Label(hostPath.Name)) != 0 {
			return fmt.Errorf("invalid host-path volume name %q", hostPath.Name)
		}
		if !path.IsAbs(hostPath.HostPath) || path.Clean(hostPath.HostPath) != hostPath.HostPath || hostPath.HostPath == "/" {
			return fmt.Errorf("host path %q must be a clean absolute path narrower than root", hostPath.HostPath)
		}
		if !path.IsAbs(hostPath.MountPath) || path.Clean(hostPath.MountPath) != hostPath.MountPath || hostPath.MountPath == "/" {
			return fmt.Errorf("mount path %q must be a clean absolute path narrower than root", hostPath.MountPath)
		}
		if hostPath.Type != corev1.HostPathFile && hostPath.Type != corev1.HostPathDirectory {
			return fmt.Errorf("host path %q must require an existing File or Directory", hostPath.HostPath)
		}
		for value, seen := range map[string]map[string]struct{}{
			hostPath.Name: seenNames, hostPath.HostPath: seenHostPaths, hostPath.MountPath: seenMountPaths,
		} {
			if _, duplicate := seen[value]; duplicate {
				return fmt.Errorf("duplicate host-access identity %q", value)
			}
			seen[value] = struct{}{}
		}
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

func configuredPolicies(specs []actuatorcore.ActuationPolicySpec) ([]actuatorcore.ActuationPolicySpec, error) {
	if len(specs) == 0 {
		return nil, errors.New("actuationPolicies must contain at least one policy")
	}
	seenNames := make(map[string]struct{}, len(specs))
	seenSelectors := make(map[string]struct{}, len(specs))
	policies := make([]actuatorcore.ActuationPolicySpec, 0, len(specs))
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			return nil, fmt.Errorf("validate configured actuation policy %q: %w", spec.Name, err)
		}
		if _, duplicate := seenNames[spec.Name]; duplicate {
			return nil, fmt.Errorf("duplicate configured actuation-policy name %q", spec.Name)
		}
		seenNames[spec.Name] = struct{}{}
		selector := spec.DeviceClass + "\x00" + spec.PowerMode + "\x00" + spec.ActuatorKind
		if _, duplicate := seenSelectors[selector]; duplicate {
			return nil, fmt.Errorf("duplicate configured actuation-policy selector %q", selector)
		}
		seenSelectors[selector] = struct{}{}
		policies = append(policies, spec)
	}
	return policies, nil
}
