package evidencestore

import (
	"reflect"
	"testing"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestBuildDeployment(t *testing.T) {
	instance := validInstance()
	config := validConfig()
	config.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
	}

	deployment := buildDeployment(instance, config)
	if deployment.Spec.Template.Labels[profilingrun.MeasurementWorkloadLabelKey] != profilingrun.SystemRole {
		t.Fatalf("Pod labels = %#v", deployment.Spec.Template.Labels)
	}
	if deployment.Namespace != instance.Namespace || deployment.Name != instance.DeploymentName {
		t.Fatalf("Deployment key = %s/%s", deployment.Namespace, deployment.Name)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 ||
		deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("Deployment lifecycle = %#v", deployment.Spec)
	}
	if !reflect.DeepEqual(deployment.Spec.Selector.MatchLabels, selectorLabels(instance.SystemName)) {
		t.Fatalf("selector = %#v", deployment.Spec.Selector.MatchLabels)
	}
	pod := deployment.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("service account token is mounted")
	}
	if pod.SecurityContext == nil || pod.SecurityContext.SeccompProfile == nil ||
		pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("Pod security context = %#v", pod.SecurityContext)
	}
	container := pod.Containers[0]
	if container.Image != config.Image || !reflect.DeepEqual(container.Resources, config.Resources) {
		t.Fatalf("store container = %#v", container)
	}
	if container.SecurityContext == nil || container.SecurityContext.RunAsNonRoot == nil ||
		!*container.SecurityContext.RunAsNonRoot || container.SecurityContext.ReadOnlyRootFilesystem == nil ||
		!*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatalf("container security context = %#v", container.SecurityContext)
	}
	if !hasSecretEnv(container.Env, "ROOT_ACCESS_KEY_ID", instance.CredentialsSecretName, accessKeySecretKey) ||
		!hasSecretEnv(container.Env, "ROOT_SECRET_ACCESS_KEY", instance.CredentialsSecretName, secretKeySecretKey) {
		t.Fatalf("credential env = %#v", container.Env)
	}
	if container.ReadinessProbe.HTTPGet.Path != healthPath ||
		container.ReadinessProbe.HTTPGet.Port != intstr.FromString(s3PortName) {
		t.Fatalf("readiness probe = %#v", container.ReadinessProbe)
	}
	if pod.Volumes[0].PersistentVolumeClaim.ClaimName != instance.PersistentVolumeClaimName {
		t.Fatalf("volume = %#v", pod.Volumes[0])
	}
}

func TestBuildService(t *testing.T) {
	instance := validInstance()
	service := buildService(instance)
	if service.Namespace != instance.Namespace || service.Name != instance.ServiceName {
		t.Fatalf("Service key = %s/%s", service.Namespace, service.Name)
	}
	if service.Spec.Type != corev1.ServiceTypeClusterIP ||
		!reflect.DeepEqual(service.Spec.Selector, selectorLabels(instance.SystemName)) {
		t.Fatalf("Service spec = %#v", service.Spec)
	}
	if _, present := service.Labels[edgeMeshServiceProxyLabel]; !present {
		t.Fatalf("Service labels = %#v", service.Labels)
	}
	port := service.Spec.Ports[0]
	if port.Port != s3Port || port.TargetPort != intstr.FromString(s3PortName) {
		t.Fatalf("Service port = %#v", port)
	}
}

func TestBuildPersistentVolumeClaim(t *testing.T) {
	instance := validInstance()
	config := validConfig()
	claim := buildPersistentVolumeClaim(instance, config)
	if claim.Namespace != instance.Namespace || claim.Name != instance.PersistentVolumeClaimName {
		t.Fatalf("PersistentVolumeClaim key = %s/%s", claim.Namespace, claim.Name)
	}
	if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != config.StorageClassName {
		t.Fatalf("StorageClassName = %v", claim.Spec.StorageClassName)
	}
	if len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("AccessModes = %v", claim.Spec.AccessModes)
	}
	if claim.Spec.Resources.Requests.Storage().Cmp(config.StorageSize) != 0 {
		t.Fatalf("storage request = %s", claim.Spec.Resources.Requests.Storage().String())
	}

	config.StorageClassName = ""
	if defaultClassClaim := buildPersistentVolumeClaim(instance, config); defaultClassClaim.Spec.StorageClassName != nil {
		t.Fatalf("default StorageClassName = %v", defaultClassClaim.Spec.StorageClassName)
	}
}

func validInstance() Instance {
	return Instance{
		SystemName:                testSystemName,
		Namespace:                 "chill-system",
		DeploymentName:            testEvidenceStoreName,
		ServiceName:               testEvidenceStoreName,
		ConfigMapName:             "research-evidence-store-config",
		ConfigMapKey:              DefaultConfigKey,
		CredentialsSecretName:     "research-evidence-store-credentials",
		PersistentVolumeClaimName: testEvidenceStoreName,
		BootstrapJobName:          "research-evidence-store-bootstrap",
	}
}

func hasSecretEnv(env []corev1.EnvVar, name, secretName, key string) bool {
	for _, variable := range env {
		if variable.Name == name && variable.ValueFrom != nil && variable.ValueFrom.SecretKeyRef != nil &&
			variable.ValueFrom.SecretKeyRef.Name == secretName && variable.ValueFrom.SecretKeyRef.Key == key {
			return true
		}
	}
	return false
}
