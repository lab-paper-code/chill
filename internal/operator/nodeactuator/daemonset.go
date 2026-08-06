package nodeactuator

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/lab-paper-code/chill/internal/component"
)

const containerName = component.NodeActuator

func buildDaemonSet(options Options, config Config) (*appsv1.DaemonSet, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	policyDigests, err := configuredPolicyDigests(config.ActuationPolicies)
	if err != nil {
		return nil, err
	}
	selectorLabels := map[string]string{
		"app.kubernetes.io/name":      component.DefaultSystemName,
		"app.kubernetes.io/instance":  options.SystemName,
		"app.kubernetes.io/component": component.NodeActuator,
	}
	labels := make(map[string]string, len(selectorLabels)+2)
	for key, value := range selectorLabels {
		labels[key] = value
	}
	labels["app.kubernetes.io/managed-by"] = "chill-operator"
	labels["app.kubernetes.io/part-of"] = component.DefaultSystemName
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: options.Namespace, Name: options.DaemonSetName, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector:       &metav1.LabelSelector{MatchLabels: selectorLabels},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: selectorLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName: config.ServiceAccountName,
					NodeSelector:       config.NodeSelector, Affinity: config.Affinity,
					Tolerations: config.Tolerations, SecurityContext: config.PodSecurityContext,
					Volumes: hostVolumes(config),
					Containers: []corev1.Container{{
						Name: containerName, Image: config.Image, ImagePullPolicy: config.ImagePullPolicy,
						Command: []string{"/node-actuator"}, Args: nodeActuatorArgs(options, config, policyDigests),
						Env: nodeIdentityEnvironment(), SecurityContext: config.SecurityContext,
						VolumeMounts:   hostVolumeMounts(config),
						Resources:      config.Resources,
						LivenessProbe:  httpProbe("healthz", config.HealthProbePort),
						ReadinessProbe: httpProbe("readyz", config.HealthProbePort),
					}},
				},
			},
		},
	}, nil
}

func nodeActuatorArgs(options Options, config Config, policyDigests []string) []string {
	args := []string{
		"--watch-namespace=" + options.Namespace,
		"--lease-namespace=" + options.Namespace,
		fmt.Sprintf("--lease-duration-seconds=%d", config.LeaseDurationSeconds),
		"--safety-valid-for=" + config.SafetyValidFor,
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
	for _, namespace := range config.AllowedNamespaces {
		args = append(args, "--allowed-namespace="+namespace)
	}
	for _, digest := range policyDigests {
		args = append(args, "--allowed-policy-digest="+digest)
	}
	for _, argument := range config.JetsonNVPModel.Command {
		args = append(args, "--jetson-nvpmodel-command="+argument)
	}
	args = append(args, "--jetson-nvpmodel-timeout="+config.JetsonNVPModel.CommandTimeout)
	return args
}

func hostVolumes(config Config) []corev1.Volume {
	volumes := make([]corev1.Volume, 0, len(config.JetsonNVPModel.HostPaths))
	for _, hostPath := range config.JetsonNVPModel.HostPaths {
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

func hostVolumeMounts(config Config) []corev1.VolumeMount {
	mounts := make([]corev1.VolumeMount, 0, len(config.JetsonNVPModel.HostPaths))
	for _, hostPath := range config.JetsonNVPModel.HostPaths {
		mounts = append(mounts, corev1.VolumeMount{
			Name: hostPath.Name, MountPath: hostPath.MountPath, ReadOnly: hostPath.ReadOnly,
		})
	}
	return mounts
}

func nodeIdentityEnvironment() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
		{Name: "POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}},
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
