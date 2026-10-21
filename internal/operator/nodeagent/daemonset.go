package nodeagent

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/nodeactuator/jetson"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const (
	binary              = "/node-agent"
	signatureVolumeName = "node-agent-signatures"
	hostnameLabelKey    = "kubernetes.io/hostname"
)

func buildAgentDaemonSet(options Options, config Config) *appsv1.DaemonSet {
	discovery := config.Discovery
	return daemonSet(options, options.agentDaemonSetName(), component.NodeAgent, discovery.UpdateStrategy, corev1.PodSpec{
		ServiceAccountName: discovery.ServiceAccountName,
		HostNetwork:        discovery.HostNetwork,
		DNSPolicy:          dnsPolicy(discovery.HostNetwork),
		SecurityContext:    discovery.PodSecurityContext,
		NodeSelector:       discovery.NodeSelector,
		Affinity:           discoveryAffinity(discovery),
		// Node agents stay on measurement POOL Nodes.
		Tolerations: append(slices.Clone(discovery.Tolerations), profilingrun.MeasurementToleration()),
		Containers: []corev1.Container{{
			Name:            component.NodeAgent,
			Image:           config.Image,
			ImagePullPolicy: config.ImagePullPolicy,
			Command:         []string{binary},
			Args: append([]string{
				"--host-root=" + discovery.HostRoot,
				"--interval=" + discovery.Interval,
				"--signature-file=" + discovery.SignatureFile,
			}, kubeAPIArgs(config)...),
			Env: append(nodeIdentityEnvironment(),
				corev1.EnvVar{Name: "CHILL_SYSTEM_NAME", Value: options.SystemName}),
			SecurityContext: discovery.SecurityContext,
			Resources:       discovery.Resources,
			VolumeMounts:    discoveryVolumeMounts(discovery),
		}},
		Volumes: discoveryVolumes(discovery),
	})
}

func buildActuationDaemonSet(
	options Options,
	config Config,
	classes []edgev1alpha1.DeviceClass,
) (*appsv1.DaemonSet, error) {
	if err := config.Validate(true); err != nil {
		return nil, err
	}
	actuation := config.Actuation
	affinity, err := actuationAffinity(classes, actuation.Policies)
	if err != nil {
		return nil, err
	}
	args, err := actuationArgs(config)
	if err != nil {
		return nil, err
	}
	return daemonSet(options, options.actuationDaemonSetName(), component.NodeAgentActuation, actuation.UpdateStrategy, corev1.PodSpec{
		// A restart is part of establishing a boot-established power mode, and
		// reboot(2) restarts the Node only from the host PID namespace. Outside
		// it, it would end this Pod instead.
		HostPID:            true,
		HostNetwork:        true,
		DNSPolicy:          corev1.DNSClusterFirstWithHostNet,
		ServiceAccountName: actuation.ServiceAccountName,
		Affinity:           affinity,
		// The actuation agent stays on measurement POOL Nodes; transition-cost
		// measurements need it there.
		Tolerations:     append(slices.Clone(actuation.Tolerations), profilingrun.MeasurementToleration()),
		SecurityContext: actuation.PodSecurityContext,
		Volumes:         hostVolumes(actuation.JetsonNVPModel.podHostPaths()),
		InitContainers:  bootOrderingInitContainers(config),
		Containers: []corev1.Container{{
			Name: component.NodeAgentActuation, Image: config.Image, ImagePullPolicy: config.ImagePullPolicy,
			Command: []string{binary}, Args: args,
			Env: nodeIdentityEnvironment(), SecurityContext: actuation.SecurityContext,
			VolumeMounts:   hostVolumeMounts(actuation.JetsonNVPModel.HostPaths),
			Resources:      actuation.Resources,
			LivenessProbe:  httpProbe("healthz", actuation.HealthProbePort),
			ReadinessProbe: httpProbe("readyz", actuation.HealthProbePort),
		}},
	}), nil
}

func daemonSet(
	options Options,
	name, componentName string,
	strategy appsv1.DaemonSetUpdateStrategy,
	pod corev1.PodSpec,
) *appsv1.DaemonSet {
	selectorLabels := map[string]string{
		"app.kubernetes.io/name":      component.DefaultSystemName,
		"app.kubernetes.io/instance":  options.SystemName,
		"app.kubernetes.io/component": componentName,
	}
	labels := maps.Clone(selectorLabels)
	labels["app.kubernetes.io/managed-by"] = "chill-operator"
	labels["app.kubernetes.io/part-of"] = component.DefaultSystemName
	labels[profilingrun.MeasurementWorkloadLabelKey] = profilingrun.SystemRole
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: options.Namespace, Name: name, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector:       &metav1.LabelSelector{MatchLabels: selectorLabels},
			UpdateStrategy: strategy,
			Template:       corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: pod},
		},
	}
}

// actuationAffinity selects the Nodes of every DeviceClass an actuation policy
// names: one required nodeSelectorTerm per DeviceClass nodeSelector.
func actuationAffinity(
	classes []edgev1alpha1.DeviceClass,
	policies []actuatorcore.ActuationPolicySpec,
) (*corev1.Affinity, error) {
	named := make(map[string]bool, len(policies))
	for _, policy := range policies {
		named[policy.DeviceClass] = true
	}
	classes = slices.Clone(classes)
	slices.SortFunc(classes, func(a, b edgev1alpha1.DeviceClass) int { return cmp.Compare(a.Name, b.Name) })
	terms := make([]corev1.NodeSelectorTerm, 0, len(named))
	for _, class := range classes {
		if !named[class.Name] || len(class.Spec.NodeSelector) == 0 {
			continue
		}
		var expressions []corev1.NodeSelectorRequirement
		for key, value := range class.Spec.NodeSelector {
			expressions = append(expressions, corev1.NodeSelectorRequirement{
				Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{value},
			})
		}
		slices.SortFunc(expressions, func(a, b corev1.NodeSelectorRequirement) int { return cmp.Compare(a.Key, b.Key) })
		terms = append(terms, corev1.NodeSelectorTerm{MatchExpressions: expressions})
	}
	if len(terms) == 0 {
		return nil, errors.New("no DeviceClass named by the actuation policies selects Nodes")
	}
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: terms},
	}}, nil
}

func dnsPolicy(hostNetwork bool) corev1.DNSPolicy {
	if hostNetwork {
		return corev1.DNSClusterFirstWithHostNet
	}
	return corev1.DNSClusterFirst
}

func discoveryAffinity(config DiscoveryConfig) *corev1.Affinity {
	var affinity *corev1.Affinity
	if config.Affinity != nil {
		affinity = config.Affinity.DeepCopy()
	}
	if len(config.ExcludeNodeNames) == 0 {
		return affinity
	}
	if affinity == nil {
		affinity = &corev1.Affinity{}
	}
	if affinity.NodeAffinity == nil {
		affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key:      hostnameLabelKey,
				Operator: corev1.NodeSelectorOpNotIn,
				Values:   slices.Clone(config.ExcludeNodeNames),
			}},
		}},
	}
	return affinity
}

func kubeAPIArgs(config Config) []string {
	var args []string
	if config.KubeAPIServer != "" {
		args = append(args, "--kube-api-server="+config.KubeAPIServer)
	}
	return append(args,
		"--kube-api-token-file="+config.KubeAPITokenFile,
		"--kube-api-ca-file="+config.KubeAPICAFile,
	)
}

func actuationArgs(config Config) ([]string, error) {
	actuation := config.Actuation
	args := []string{
		"--actuate",
		"--metrics-bind-address=" + actuation.MetricsBindAddress,
		fmt.Sprintf("--health-probe-bind-address=:%d", actuation.HealthProbePort),
	}
	args = append(args, kubeAPIArgs(config)...)
	for _, policy := range actuation.Policies {
		encoded, err := json.Marshal(policy)
		if err != nil {
			return nil, fmt.Errorf("encode allowed actuation policy %q: %w", policy.Name, err)
		}
		args = append(args, "--allowed-policy="+string(encoded))
	}
	for _, argument := range actuation.JetsonNVPModel.Command {
		args = append(args, "--jetson-nvpmodel-command="+argument)
	}
	return append(args, "--jetson-nvpmodel-timeout="+actuation.JetsonNVPModel.CommandTimeout), nil
}

func discoveryVolumeMounts(config DiscoveryConfig) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{{
		Name: signatureVolumeName, MountPath: path.Dir(config.SignatureFile), ReadOnly: true,
	}}
	for _, hostPath := range config.HostPaths {
		mounts = append(mounts, corev1.VolumeMount{
			Name: hostPath.Name, MountPath: path.Join(config.HostRoot, hostPath.MountPath), ReadOnly: true,
		})
	}
	return mounts
}

func discoveryVolumes(config DiscoveryConfig) []corev1.Volume {
	volumes := []corev1.Volume{{
		Name: signatureVolumeName,
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: config.SignatureConfigMapName},
			Items:                []corev1.KeyToPath{{Key: config.SignatureConfigMapKey, Path: path.Base(config.SignatureFile)}},
		}},
	}}
	for _, hostPath := range config.HostPaths {
		volumes = append(volumes, corev1.Volume{
			Name: hostPath.Name,
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: hostPath.HostPath, Type: ptr.To(corev1.HostPathDirectory),
			}},
		})
	}
	return volumes
}

func hostVolumes(hostPaths []HostPathMount) []corev1.Volume {
	volumes := make([]corev1.Volume, 0, len(hostPaths))
	for _, hostPath := range hostPaths {
		volumes = append(volumes, corev1.Volume{
			Name: hostPath.Name,
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: hostPath.HostPath, Type: ptr.To(hostPath.Type),
			}},
		})
	}
	return volumes
}

func hostVolumeMounts(hostPaths []HostPathMount) []corev1.VolumeMount {
	mounts := make([]corev1.VolumeMount, 0, len(hostPaths))
	for _, hostPath := range hostPaths {
		mounts = append(mounts, corev1.VolumeMount{
			Name: hostPath.Name, MountPath: hostPath.MountPath, ReadOnly: hostPath.ReadOnly,
		})
	}
	return mounts
}

// bootOrderingInitContainers writes the nvidia-cdi-refresh.service ordering
// drop-in before the actuation agent starts. It holds no capability: root owns
// the systemd directory it writes. systemd reads the drop-in at the next boot,
// which is the only boot the ordering affects, so no daemon-reload is needed.
func bootOrderingInitContainers(config Config) []corev1.Container {
	if !config.Actuation.JetsonNVPModel.EnsureBootOrdering {
		return nil
	}
	return []corev1.Container{{
		Name: "ensure-boot-ordering", Image: config.Image, ImagePullPolicy: config.ImagePullPolicy,
		Command: []string{binary, jetson.EnsureBootOrderingCommand},
		SecurityContext: &corev1.SecurityContext{
			Privileged: ptr.To(false), AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		VolumeMounts: hostVolumeMounts(bootOrderingHostPaths),
		Resources:    config.Actuation.Resources,
	}}
}

func nodeIdentityEnvironment() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
	}
}

func httpProbe(path string, port int32) *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
		Path: "/" + path, Port: intstr.FromInt32(port),
	}}}
}
