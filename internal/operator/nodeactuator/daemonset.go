package nodeactuator

import (
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/lab-paper-code/chill/internal/component"
	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/nodeactuator/jetson"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const containerName = component.NodeActuator

func buildDaemonSet(options Options, config Config) (*appsv1.DaemonSet, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	policies, err := configuredPolicies(config.ActuationPolicies)
	if err != nil {
		return nil, err
	}
	args, err := nodeActuatorArgs(options, config, policies)
	if err != nil {
		return nil, err
	}
	selectorLabels := map[string]string{
		"app.kubernetes.io/name":      component.DefaultSystemName,
		"app.kubernetes.io/instance":  options.SystemName,
		"app.kubernetes.io/component": component.NodeActuator,
	}
	labels := make(map[string]string, len(selectorLabels)+3)
	for key, value := range selectorLabels {
		labels[key] = value
	}
	labels["app.kubernetes.io/managed-by"] = "chill-operator"
	labels["app.kubernetes.io/part-of"] = component.DefaultSystemName
	labels[profilingrun.MeasurementWorkloadLabelKey] = profilingrun.SystemRole
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: options.Namespace, Name: options.DaemonSetName, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector:       &metav1.LabelSelector{MatchLabels: selectorLabels},
			UpdateStrategy: config.UpdateStrategy,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					// A restart is part of establishing a boot-established power
					// mode, and reboot(2) restarts the Node only from the host PID
					// namespace. Outside it, it would end this Pod instead.
					HostPID:            true,
					HostNetwork:        true,
					DNSPolicy:          corev1.DNSClusterFirstWithHostNet,
					ServiceAccountName: config.ServiceAccountName,
					NodeSelector:       config.NodeSelector, Affinity: config.Affinity,
					Tolerations: config.Tolerations, SecurityContext: config.PodSecurityContext,
					Volumes:        hostVolumes(config.JetsonNVPModel.podHostPaths()),
					InitContainers: bootOrderingInitContainers(config),
					Containers: []corev1.Container{{
						Name: containerName, Image: config.Image, ImagePullPolicy: config.ImagePullPolicy,
						Command: []string{"/node-actuator"}, Args: args,
						Env: nodeIdentityEnvironment(), SecurityContext: config.SecurityContext,
						VolumeMounts:   hostVolumeMounts(config.JetsonNVPModel.HostPaths),
						Resources:      config.Resources,
						LivenessProbe:  httpProbe("healthz", config.HealthProbePort),
						ReadinessProbe: httpProbe("readyz", config.HealthProbePort),
					}},
				},
			},
		},
	}, nil
}

func nodeActuatorArgs(
	options Options,
	config Config,
	policies []actuatorcore.ActuationPolicySpec,
) ([]string, error) {
	args := []string{
		"--watch-namespace=" + options.Namespace,
		"--lease-namespace=" + options.Namespace,
		fmt.Sprintf("--lease-duration-seconds=%d", config.LeaseDurationSeconds),
		"--metrics-bind-address=" + config.MetricsBindAddress,
		fmt.Sprintf("--health-probe-bind-address=:%d", config.HealthProbePort),
	}
	if config.KubeAPIServer != "" {
		args = append(args, "--kube-api-server="+config.KubeAPIServer)
	}
	args = append(args,
		"--kube-api-token-file="+config.KubeAPITokenFile,
		"--kube-api-ca-file="+config.KubeAPICAFile,
	)
	for _, policy := range policies {
		encoded, err := json.Marshal(policy)
		if err != nil {
			return nil, fmt.Errorf("encode allowed actuation policy %q: %w", policy.Name, err)
		}
		args = append(args, "--allowed-policy="+string(encoded))
	}
	for _, argument := range config.JetsonNVPModel.Command {
		args = append(args, "--jetson-nvpmodel-command="+argument)
	}
	args = append(args, "--jetson-nvpmodel-timeout="+config.JetsonNVPModel.CommandTimeout)
	return args, nil
}

func hostVolumes(hostPaths []HostPathMount) []corev1.Volume {
	volumes := make([]corev1.Volume, 0, len(hostPaths))
	for _, hostPath := range hostPaths {
		pathType := hostPath.Type
		volumes = append(volumes, corev1.Volume{
			Name: hostPath.Name,
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: hostPath.HostPath, Type: &pathType,
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
// drop-in before the actuator starts. It holds no capability: root owns the
// systemd directory it writes. systemd reads the drop-in at the next boot,
// which is the only boot the ordering affects, so no daemon-reload is needed.
func bootOrderingInitContainers(config Config) []corev1.Container {
	if !config.JetsonNVPModel.EnsureBootOrdering {
		return nil
	}
	return []corev1.Container{{
		Name: "ensure-boot-ordering", Image: config.Image, ImagePullPolicy: config.ImagePullPolicy,
		Command: []string{"/node-actuator", jetson.EnsureBootOrderingCommand},
		SecurityContext: &corev1.SecurityContext{
			Privileged: ptr.To(false), AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		VolumeMounts: hostVolumeMounts(bootOrderingHostPaths),
		Resources:    config.Resources,
	}}
}

func nodeIdentityEnvironment() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
	}
}

func httpProbe(path string, port int32) *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
		Path: "/" + path, Port: intstrFromInt32(port),
	}}}
}

func intstrFromInt32(value int32) intstr.IntOrString {
	return intstr.FromInt32(value)
}
