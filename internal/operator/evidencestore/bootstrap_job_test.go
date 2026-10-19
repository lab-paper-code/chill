package evidencestore

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/lab-paper-code/chill/internal/component"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const (
	testConfigResourceVersion        = "7"
	changedTestConfigResourceVersion = "8"
)

func TestBuildBootstrapJob(t *testing.T) {
	instance := validInstance()
	config := validConfig()
	config.BootstrapResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
	}

	job := buildBootstrapJob(instance, config, testConfigResourceVersion)
	if job.Spec.Template.Labels[profilingrun.MeasurementWorkloadLabelKey] != profilingrun.SystemRole {
		t.Fatalf("Pod labels = %#v", job.Spec.Template.Labels)
	}
	if job.Namespace != instance.Namespace || job.Name != instance.BootstrapJobName {
		t.Fatalf("Job key = %s/%s", job.Namespace, job.Name)
	}
	if len(job.OwnerReferences) != 0 {
		t.Fatalf("builder ownerReferences = %#v", job.OwnerReferences)
	}
	if job.Annotations[component.EvidenceStoreConfigResourceVersionAnnotation] != testConfigResourceVersion {
		t.Fatalf("config resourceVersion annotation = %q", job.Annotations[component.EvidenceStoreConfigResourceVersionAnnotation])
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != bootstrapDeadlineSeconds ||
		job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != bootstrapBackoffLimit {
		t.Fatalf("Job bounds = %#v", job.Spec)
	}
	if job.Spec.TTLSecondsAfterFinished != nil {
		t.Fatalf("TTLSecondsAfterFinished = %d", *job.Spec.TTLSecondsAfterFinished)
	}

	assertBootstrapPod(t, job.Spec.Template.Spec, instance, config)
}

func assertBootstrapPod(t *testing.T, pod corev1.PodSpec, instance Instance, config Config) {
	t.Helper()
	if pod.RestartPolicy != corev1.RestartPolicyNever ||
		pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken ||
		pod.EnableServiceLinks == nil || *pod.EnableServiceLinks {
		t.Fatalf("bootstrap Pod lifecycle = %#v", pod)
	}
	if pod.SecurityContext == nil || pod.SecurityContext.SeccompProfile == nil ||
		pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("bootstrap Pod security = %#v", pod.SecurityContext)
	}

	container := pod.Containers[0]
	if container.Image != config.BootstrapImage || !reflect.DeepEqual(container.Args, []string{"ensure-bucket"}) ||
		!reflect.DeepEqual(container.Resources, config.BootstrapResources) {
		t.Fatalf("bootstrap container = %#v", container)
	}
	if !hasLiteralEnv(container.Env, "STORE_ENDPOINT", "http://research-evidence-store:9000") ||
		!hasLiteralEnv(container.Env, "STORE_BUCKET", config.Bucket) ||
		!hasLiteralEnv(container.Env, "AWS_DEFAULT_REGION", config.Region) {
		t.Fatalf("bootstrap environment = %#v", container.Env)
	}
	if !hasSecretEnv(container.Env, "AWS_ACCESS_KEY_ID", instance.CredentialsSecretName, accessKeySecretKey) ||
		!hasSecretEnv(container.Env, "AWS_SECRET_ACCESS_KEY", instance.CredentialsSecretName, secretKeySecretKey) {
		t.Fatalf("bootstrap credentials = %#v", container.Env)
	}
	if container.SecurityContext == nil || container.SecurityContext.RunAsNonRoot == nil ||
		!*container.SecurityContext.RunAsNonRoot || container.SecurityContext.ReadOnlyRootFilesystem == nil ||
		!*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatalf("bootstrap container security = %#v", container.SecurityContext)
	}
}

func hasLiteralEnv(env []corev1.EnvVar, name, value string) bool {
	for _, variable := range env {
		if variable.Name == name && variable.Value == value && variable.ValueFrom == nil {
			return true
		}
	}
	return false
}
