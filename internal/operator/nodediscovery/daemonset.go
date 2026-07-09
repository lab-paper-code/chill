package nodediscovery

import (
	"path"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lab-paper-code/chill/internal/component"
)

const (
	containerName       = component.NodeDiscovery
	signatureVolumeName = "node-discovery-signatures"
	hostnameLabelKey    = "kubernetes.io/hostname"
)

func buildDaemonSet(options Options, config Config) *appsv1.DaemonSet {
	labels := workloadLabels(options.SystemName)
	selectorLabels := workloadSelectorLabels(options.SystemName)

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: options.Namespace,
			Name:      options.DaemonSetName,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			UpdateStrategy: config.UpdateStrategy,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: selectorLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName: config.ServiceAccountName,
					SecurityContext:    config.PodSecurityContext,
					NodeSelector:       config.NodeSelector,
					Affinity:           buildAffinity(config),
					Tolerations:        config.Tolerations,
					Containers: []corev1.Container{
						{
							Name:            containerName,
							Image:           config.Image,
							ImagePullPolicy: config.ImagePullPolicy,
							Command:         []string{"/node-discovery"},
							Args:            nodeDiscoveryArgs(options, config),
							Env:             nodeDiscoveryEnv(options, config),
							SecurityContext: config.SecurityContext,
							Resources:       config.Resources,
							VolumeMounts:    volumeMounts(config),
						},
					},
					Volumes: volumes(config),
				},
			},
		},
	}
}

func workloadLabels(systemName string) map[string]string {
	labels := workloadSelectorLabels(systemName)
	labels["app.kubernetes.io/managed-by"] = "chill-operator"
	labels["app.kubernetes.io/part-of"] = component.DefaultSystemName
	return labels
}

func workloadSelectorLabels(systemName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      component.DefaultSystemName,
		"app.kubernetes.io/instance":  systemName,
		"app.kubernetes.io/component": component.NodeDiscovery,
	}
}

func buildAffinity(config Config) *corev1.Affinity {
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
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      hostnameLabelKey,
						Operator: corev1.NodeSelectorOpNotIn,
						Values:   append([]string(nil), config.ExcludeNodeNames...),
					},
				},
			},
		},
	}
	return affinity
}

func nodeDiscoveryArgs(options Options, config Config) []string {
	args := []string{
		"--system-name=" + options.SystemName,
		"--host-root=" + config.HostRoot,
		"--interval=" + config.Interval,
		"--signature-file=" + config.SignatureFile,
	}
	if config.CleanupOnExit {
		args = append(args, "--cleanup-on-exit", "--cleanup-timeout="+config.CleanupTimeout)
	}
	if config.KubeAPIServer != "" {
		args = append(args, "--kube-api-server="+config.KubeAPIServer)
	}
	args = append(args,
		"--kube-api-token-file="+config.KubeAPITokenFile,
		"--kube-api-ca-file="+config.KubeAPICAFile,
	)
	return args
}

func nodeDiscoveryEnv(options Options, config Config) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name: "NODE_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
			},
		},
		{
			Name:  "CHILL_SYSTEM_NAME",
			Value: options.SystemName,
		},
	}
	if config.KubeAPIServer != "" {
		env = append(env, corev1.EnvVar{
			Name:  "CHILL_KUBE_API_SERVER",
			Value: config.KubeAPIServer,
		})
	}
	return env
}

func volumeMounts(config Config) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{
			Name:      signatureVolumeName,
			MountPath: path.Dir(config.SignatureFile),
			ReadOnly:  true,
		},
	}
	for _, hostPath := range config.HostPaths {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      hostPath.Name,
			MountPath: path.Join(config.HostRoot, hostPath.MountPath),
			ReadOnly:  true,
		})
	}
	return mounts
}

func volumes(config Config) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: signatureVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: config.SignatureConfigMapName},
					Items: []corev1.KeyToPath{
						{
							Key:  config.SignatureConfigMapKey,
							Path: path.Base(config.SignatureFile),
						},
					},
				},
			},
		},
	}
	for _, hostPath := range config.HostPaths {
		volumes = append(volumes, corev1.Volume{
			Name: hostPath.Name,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: hostPath.HostPath,
					Type: hostPathDirectory(),
				},
			},
		})
	}
	return volumes
}

func hostPathDirectory() *corev1.HostPathType {
	pathType := corev1.HostPathDirectory
	return &pathType
}
