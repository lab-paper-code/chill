package evidencestore

import (
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/lab-paper-code/chill/internal/component"
)

const (
	storeContainerName = component.EvidenceStore
	dataVolumeName     = "data"
	dataMountPath      = "/data"
	s3PortName         = "s3"
	s3Port             = component.EvidenceStoreS3Port
	healthPath         = "/_/health"
	accessKeySecretKey = "access-key"
	secretKeySecretKey = "secret-key"
)

func buildDeployment(instance Instance, config Config) *appsv1.Deployment {
	labels := operandLabels(instance.SystemName)
	selectors := selectorLabels(instance.SystemName)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: instance.Namespace,
			Name:      instance.DeploymentName,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: selectors},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						"kubectl.kubernetes.io/default-container": storeContainerName,
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext:              storePodSecurityContext(),
					Containers:                   []corev1.Container{buildStoreContainer(instance, config)},
					Volumes: []corev1.Volume{
						{
							Name: dataVolumeName,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: instance.PersistentVolumeClaimName,
								},
							},
						},
					},
				},
			},
		},
	}
}

func buildStoreContainer(instance Instance, config Config) corev1.Container {
	return corev1.Container{
		Name:            storeContainerName,
		Image:           config.Image,
		ImagePullPolicy: config.ImagePullPolicy,
		Env: []corev1.EnvVar{
			{Name: "VGW_BACKEND", Value: "posix"},
			{Name: "VGW_BACKEND_ARGS", Value: dataMountPath},
			{Name: "VGW_PORT", Value: ":" + strconv.Itoa(int(s3Port))},
			{Name: "VGW_REGION", Value: config.Region},
			{Name: "VGW_HEALTH", Value: healthPath},
			secretEnv("ROOT_ACCESS_KEY_ID", instance.CredentialsSecretName, accessKeySecretKey),
			secretEnv("ROOT_SECRET_ACCESS_KEY", instance.CredentialsSecretName, secretKeySecretKey),
		},
		Ports:           []corev1.ContainerPort{{Name: s3PortName, ContainerPort: s3Port, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe:  httpProbe(5, 2, 6),
		LivenessProbe:   httpProbe(10, 2, 3),
		Resources:       config.Resources,
		SecurityContext: restrictedContainerSecurityContext(),
		VolumeMounts:    []corev1.VolumeMount{{Name: dataVolumeName, MountPath: dataMountPath}},
	}
}

func storePodSecurityContext() *corev1.PodSecurityContext {
	securityContext := runtimeDefaultPodSecurityContext()
	securityContext.FSGroup = ptr.To[int64](1000)
	securityContext.FSGroupChangePolicy = ptr.To(corev1.FSGroupChangeOnRootMismatch)
	return securityContext
}

func secretEnv(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
		}},
	}
}

func httpProbe(period, timeout, failures int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: healthPath,
			Port: intstr.FromString(s3PortName),
		}},
		PeriodSeconds:    period,
		TimeoutSeconds:   timeout,
		FailureThreshold: failures,
	}
}
