package nodeagent

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/nodeactuator/jetson"
	"github.com/lab-paper-code/chill/internal/nodeactuator/nvidia"
)

// rebootCapability is the one capability the actuation agent may hold. A power
// mode the device establishes at boot is reached by restarting the Node, and
// this is the narrowest privilege that permits it. Nothing else is allowed
// alongside it.
const rebootCapability = corev1.Capability("SYS_BOOT")

// Config is the rendered node agent contract read by the operator. Discovery
// runs on every Node; Actuation is present only when the ChillSystem enables it.
type Config struct {
	Image            string            `json:"image"`
	ImagePullPolicy  corev1.PullPolicy `json:"imagePullPolicy"`
	KubeAPIServer    string            `json:"kubeAPIServer,omitempty"`
	KubeAPITokenFile string            `json:"kubeAPITokenFile"`
	KubeAPICAFile    string            `json:"kubeAPICAFile"`
	Discovery        DiscoveryConfig   `json:"discovery"`
	Actuation        *ActuationConfig  `json:"actuation,omitempty"`
}

// DiscoveryConfig is the unprivileged DaemonSet that publishes hardware
// signature and online-CPU metadata on every Node.
type DiscoveryConfig struct {
	ServiceAccountName     string                         `json:"serviceAccountName"`
	HostRoot               string                         `json:"hostRoot"`
	Interval               string                         `json:"interval"`
	SignatureFile          string                         `json:"signatureFile"`
	SignatureConfigMapName string                         `json:"signatureConfigMapName"`
	SignatureConfigMapKey  string                         `json:"signatureConfigMapKey"`
	HostNetwork            bool                           `json:"hostNetwork,omitempty"`
	NodeSelector           map[string]string              `json:"nodeSelector,omitempty"`
	ExcludeNodeNames       []string                       `json:"excludeNodeNames,omitempty"`
	Affinity               *corev1.Affinity               `json:"affinity,omitempty"`
	Tolerations            []corev1.Toleration            `json:"tolerations,omitempty"`
	Resources              corev1.ResourceRequirements    `json:"resources,omitempty"`
	PodSecurityContext     *corev1.PodSecurityContext     `json:"podSecurityContext,omitempty"`
	SecurityContext        *corev1.SecurityContext        `json:"securityContext,omitempty"`
	HostPaths              []ReadOnlyHostPath             `json:"hostPaths,omitempty"`
	UpdateStrategy         appsv1.DaemonSetUpdateStrategy `json:"updateStrategy"`
	ResourceObservation string `json:"resourceObservation,omitempty"`
	ResourceCASecret string `json:"resourceCASecret,omitempty"`
	ResourceCAFile string `json:"resourceCAFile,omitempty"`
}

// ReadOnlyHostPath is a host directory mounted read-only under hostRoot.
type ReadOnlyHostPath struct {
	Name      string `json:"name"`
	HostPath  string `json:"hostPath"`
	MountPath string `json:"mountPath"`
}

// ActuationConfig is the privileged DaemonSet that applies the desired power
// mode on Nodes of the DeviceClasses named by its policies.
type ActuationConfig struct {
	ServiceAccountName   string                             `json:"serviceAccountName"`
	MetricsBindAddress   string                             `json:"metricsBindAddress"`
	HealthProbePort      int32                              `json:"healthProbePort"`
	Policies             []actuatorcore.ActuationPolicySpec `json:"policies"`
	JetsonNVPModel       *JetsonNVPModelConfig              `json:"jetsonNVPModel,omitempty"`
	NvidiaLockedGPUClock *NvidiaClockConfig                 `json:"nvidiaLockedGPUClock,omitempty"`
	Tolerations          []corev1.Toleration                `json:"tolerations,omitempty"`
	Resources            corev1.ResourceRequirements        `json:"resources,omitempty"`
	PodSecurityContext   *corev1.PodSecurityContext         `json:"podSecurityContext,omitempty"`
	SecurityContext      *corev1.SecurityContext            `json:"securityContext,omitempty"`
	UpdateStrategy       appsv1.DaemonSetUpdateStrategy     `json:"updateStrategy"`
}

type JetsonNVPModelConfig struct {
	Command        []string        `json:"command"`
	CommandTimeout string          `json:"commandTimeout"`
	HostPaths      []HostPathMount `json:"hostPaths"`
	// EnsureBootOrdering adds an init container that orders
	// nvidia-cdi-refresh.service after nvpmodel.service on hosts that have it,
	// so the boot-established power modes apply at boot.
	EnsureBootOrdering bool `json:"ensureBootOrdering"`
}

// NvidiaClockConfig is separate from Jetson host access; its operand never
// mounts Jetson state or receives SYS_BOOT/host PID access.
type NvidiaClockConfig struct {
	Command            []string                   `json:"command"`
	CommandTimeout     string                     `json:"commandTimeout"`
	Clocks             []int32                    `json:"clocks"`
	HostPaths          []HostPathMount            `json:"hostPaths"`
	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext"`
	SecurityContext    *corev1.SecurityContext    `json:"securityContext"`
	RuntimeClassName   string                     `json:"runtimeClassName"`
	NodeNames          []string                   `json:"nodeNames,omitempty"`
}

func (c ActuationConfig) forKind(kind string) ActuationConfig {
	policies := make([]actuatorcore.ActuationPolicySpec, 0, len(c.Policies))
	for _, policy := range c.Policies {
		if policy.ActuatorKind == kind {
			policies = append(policies, policy)
		}
	}
	c.Policies = policies
	if kind == actuatorcore.ActuatorKindNvidiaLockedGPUClock && c.NvidiaLockedGPUClock != nil {
		c.PodSecurityContext = c.NvidiaLockedGPUClock.PodSecurityContext
		c.SecurityContext = c.NvidiaLockedGPUClock.SecurityContext
	}
	return c
}

// bootOrderingHostPaths scope the boot-ordering init container to the systemd
// unit directories: the packaged one read-only, the local one writable for the
// drop-in.
var bootOrderingHostPaths = []HostPathMount{
	{Name: "host-systemd-etc", HostPath: "/etc/systemd/system", MountPath: jetson.HostSystemdEtcDir, Type: corev1.HostPathDirectory},
	{Name: "host-systemd-lib", HostPath: "/lib/systemd/system", MountPath: jetson.HostSystemdLibDir, Type: corev1.HostPathDirectory, ReadOnly: true},
}

// podHostPaths is every host path the actuation Pod mounts.
func (c JetsonNVPModelConfig) podHostPaths() []HostPathMount {
	if !c.EnsureBootOrdering {
		return c.HostPaths
	}
	return append(slices.Clone(c.HostPaths), bootOrderingHostPaths...)
}

type HostPathMount struct {
	Name      string              `json:"name"`
	HostPath  string              `json:"hostPath"`
	MountPath string              `json:"mountPath"`
	Type      corev1.HostPathType `json:"type"`
	ReadOnly  bool                `json:"readOnly"`
}

// Validate checks the shared and discovery contract, and the actuation
// contract when requireActuation is set.
func (c Config) Validate(requireActuation bool) error {
	var problems []string
	for name, value := range map[string]string{
		"image": c.Image, "imagePullPolicy": string(c.ImagePullPolicy),
		"kubeAPITokenFile": c.KubeAPITokenFile, "kubeAPICAFile": c.KubeAPICAFile,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			problems = append(problems, name+" is required without surrounding whitespace")
		}
	}
	if strings.TrimSpace(c.KubeAPIServer) != c.KubeAPIServer {
		problems = append(problems, "kubeAPIServer must not have surrounding whitespace")
	}
	problems = append(problems, c.Discovery.problems()...)
	if requireActuation {
		if c.Actuation == nil {
			problems = append(problems, "actuation config is required when actuation is enabled")
		} else {
			problems = append(problems, c.Actuation.problems()...)
		}
	}
	if len(problems) != 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (c DiscoveryConfig) problems() []string {
	var problems []string
	if c.ResourceObservation != "" && (c.ResourceCASecret == "" || !path.IsAbs(c.ResourceCAFile)) {
		problems = append(problems, "resource observation requires a CA Secret and absolute CA file")
	}
	for name, value := range map[string]string{
		"discovery.serviceAccountName": c.ServiceAccountName, "discovery.hostRoot": c.HostRoot,
		"discovery.interval": c.Interval, "discovery.signatureFile": c.SignatureFile,
		"discovery.signatureConfigMapName": c.SignatureConfigMapName,
		"discovery.signatureConfigMapKey":  c.SignatureConfigMapKey,
	} {
		if strings.TrimSpace(value) == "" {
			problems = append(problems, name+" is required")
		}
	}
	if _, err := time.ParseDuration(c.Interval); c.Interval != "" && err != nil {
		problems = append(problems, fmt.Sprintf("discovery.interval must be a duration: %v", err))
	}
	if c.UpdateStrategy.Type == "" {
		problems = append(problems, "discovery.updateStrategy.type is required")
	}
	if len(c.HostPaths) == 0 {
		problems = append(problems, "discovery.hostPaths must contain at least one mount")
	}
	for i, hostPath := range c.HostPaths {
		if strings.TrimSpace(hostPath.Name) == "" || strings.TrimSpace(hostPath.HostPath) == "" ||
			strings.TrimSpace(hostPath.MountPath) == "" {
			problems = append(problems, fmt.Sprintf("discovery.hostPaths[%d] requires name, hostPath, and mountPath", i))
		}
	}
	if len(c.ExcludeNodeNames) > 0 && c.Affinity != nil && c.Affinity.NodeAffinity != nil &&
		c.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		problems = append(problems, "discovery.excludeNodeNames cannot be combined with required nodeAffinity")
	}
	return problems
}

func (c ActuationConfig) problems() []string {
	var problems []string
	for name, value := range map[string]string{
		"actuation.serviceAccountName": c.ServiceAccountName, "actuation.metricsBindAddress": c.MetricsBindAddress,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			problems = append(problems, name+" is required without surrounding whitespace")
		}
	}
	if c.HealthProbePort < 1 || c.HealthProbePort > 65535 {
		problems = append(problems, "actuation.healthProbePort must be between 1 and 65535")
	}
	switch c.UpdateStrategy.Type {
	case appsv1.RollingUpdateDaemonSetStrategyType:
	case appsv1.OnDeleteDaemonSetStrategyType:
		if c.UpdateStrategy.RollingUpdate != nil {
			problems = append(problems, "actuation.updateStrategy.rollingUpdate must not be set with OnDelete")
		}
	default:
		problems = append(problems, "actuation.updateStrategy.type must be RollingUpdate or OnDelete")
	}
	if err := validatePolicies(c.Policies); err != nil {
		problems = append(problems, err.Error())
	}
	if err := c.validateHostAdapters(); err != nil {
		problems = append(problems, err.Error())
	}
	return problems
}

func (c ActuationConfig) validateHostAdapters() error {
	hasJetson := false
	for _, policy := range c.Policies {
		switch policy.ActuatorKind {
		case actuatorcore.ActuatorKindJetsonNVPModel:
			hasJetson = true
		case actuatorcore.ActuatorKindNvidiaLockedGPUClock:
			if err := c.validateNvidia(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no installed host adapter supports actuation-policy kind %q", policy.ActuatorKind)
		}
	}
	if !hasJetson {
		return nil
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
	if err := validateHostPaths(c.JetsonNVPModel.podHostPaths()); err != nil {
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

func (c ActuationConfig) validateNvidia() error {
	nv := c.NvidiaLockedGPUClock
	if nv == nil {
		return errors.New("nvidiaLockedGPUClock config is required by NVIDIA policies")
	}
	if len(validation.IsDNS1123Subdomain(nv.RuntimeClassName)) != 0 {
		return errors.New("NVIDIA requires an explicit valid runtimeClassName for device access")
	}
	for _, name := range nv.NodeNames {
		if len(validation.IsDNS1123Subdomain(name)) != 0 {
			return errors.New("NVIDIA nodeNames must be valid Node names")
		}
	}
	timeout, err := time.ParseDuration(nv.CommandTimeout)
	if err != nil {
		return errors.New("nvidiaLockedGPUClock.commandTimeout must be a duration")
	}
	if err := (nvidia.Options{Command: nv.Command, Timeout: timeout, Clocks: nv.Clocks}).Validate(); err != nil {
		return err
	}
	if err := validateHostPaths(nv.HostPaths); err != nil {
		return err
	}
	identity, security := nv.PodSecurityContext, nv.SecurityContext
	if identity == nil || identity.RunAsUser == nil || *identity.RunAsUser != 0 ||
		identity.RunAsNonRoot == nil || *identity.RunAsNonRoot {
		return errors.New("NVIDIA requires an explicit root Pod identity")
	}
	if security == nil || security.Privileged == nil || *security.Privileged ||
		security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem || security.Capabilities == nil ||
		len(security.Capabilities.Add) != 1 || security.Capabilities.Add[0] != "SYS_ADMIN" ||
		!slices.Contains(security.Capabilities.Drop, corev1.Capability("ALL")) {
		return errors.New("NVIDIA requires non-privileged, no-escalation, read-only-rootfs security adding only SYS_ADMIN")
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

func validatePolicies(specs []actuatorcore.ActuationPolicySpec) error {
	if len(specs) == 0 {
		return errors.New("actuation.policies must contain at least one policy")
	}
	seenNames := make(map[string]struct{}, len(specs))
	seenSelectors := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			return fmt.Errorf("validate configured actuation policy %q: %w", spec.Name, err)
		}
		if _, duplicate := seenNames[spec.Name]; duplicate {
			return fmt.Errorf("duplicate configured actuation-policy name %q", spec.Name)
		}
		seenNames[spec.Name] = struct{}{}
		selector := spec.DeviceClass + "\x00" + spec.PowerMode + "\x00" + spec.ActuatorKind
		if _, duplicate := seenSelectors[selector]; duplicate {
			return fmt.Errorf("duplicate configured actuation-policy selector %q", selector)
		}
		seenSelectors[selector] = struct{}{}
	}
	return nil
}
