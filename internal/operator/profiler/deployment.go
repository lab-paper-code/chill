package profiler

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/lab-paper-code/chill/internal/component"
)

const containerName = component.Profiler

func buildDeployment(options Options, config Config) *appsv1.Deployment {
	labels := workloadLabels(options.SystemName)
	selectorLabels := workloadSelectorLabels(options.SystemName)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: options.Namespace,
			Name:      options.DeploymentName,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &config.Replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: selectorLabels,
					Annotations: map[string]string{
						"kubectl.kubernetes.io/default-container": containerName,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: config.ServiceAccountName,
					SecurityContext:    config.PodSecurityContext,
					NodeSelector:       config.NodeSelector,
					Affinity:           config.Affinity,
					Tolerations:        config.Tolerations,
					Containers: []corev1.Container{
						{
							Name:            containerName,
							Image:           config.Image,
							ImagePullPolicy: config.ImagePullPolicy,
							Command:         []string{"/profiler"},
							Args:            profilerArgs(options, config),
							Env: []corev1.EnvVar{
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.namespace",
									}},
								},
							},
							SecurityContext: config.SecurityContext,
							Resources:       config.Resources,
							LivenessProbe:   httpProbe("/healthz", config.HealthProbePort, 15, 20),
							ReadinessProbe:  httpProbe("/readyz", config.HealthProbePort, 5, 10),
						},
					},
					TerminationGracePeriodSeconds: int64Ptr(10),
				},
			},
		},
	}
}

func profilerArgs(options Options, config Config) []string {
	args := []string{
		"--system-name=" + options.SystemName,
		"--metrics-bind-address=" + config.MetricsBindAddress,
		"--health-probe-bind-address=" + healthProbeAddress(config.HealthProbePort),
		"--leader-election-id=" + config.LeaderElectionID,
		"--fact-source-config-map=" + config.FactSource.ConfigMapName,
		"--execution-facts-key=" + config.FactSource.ExecutionFactsKey,
		"--power-sources-key=" + config.FactSource.PowerSourcesKey,
	}
	if config.LeaderElection {
		args = append(args, "--leader-elect")
	}
	return args
}

func httpProbe(path string, port, initialDelay, period int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: path,
			Port: intstr.FromInt32(port),
		}},
		InitialDelaySeconds: initialDelay,
		PeriodSeconds:       period,
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
		"app.kubernetes.io/component": component.Profiler,
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}

func deploymentKey(options Options) string {
	return fmt.Sprintf("%s/%s", options.Namespace, options.DeploymentName)
}
