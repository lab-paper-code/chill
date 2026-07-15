package evidencestore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	bootstrapContainerName      = "bootstrap"
	bootstrapDeadlineSeconds    = int64(60)
	bootstrapBackoffLimit       = int32(3)
	bootstrapSpecHashAnnotation = "edge.dacs.io/evidence-store-bootstrap-spec-hash"
)

func buildBootstrapJob(instance Instance, config Config) (*batchv1.Job, error) {
	labels := operandLabels(instance.SystemName)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: instance.Namespace,
			Name:      instance.BootstrapJobName,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			ActiveDeadlineSeconds: ptr.To(bootstrapDeadlineSeconds),
			BackoffLimit:          ptr.To(bootstrapBackoffLimit),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						"kubectl.kubernetes.io/default-container": bootstrapContainerName,
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					RestartPolicy:                corev1.RestartPolicyNever,
					SecurityContext:              runtimeDefaultPodSecurityContext(),
					Containers: []corev1.Container{
						{
							Name:            bootstrapContainerName,
							Image:           config.BootstrapImage,
							ImagePullPolicy: config.ImagePullPolicy,
							Args:            []string{"ensure-bucket"},
							Env: []corev1.EnvVar{
								{Name: "STORE_ENDPOINT", Value: bootstrapEndpoint(instance)},
								{Name: "STORE_BUCKET", Value: config.Bucket},
								secretEnv("AWS_ACCESS_KEY_ID", instance.CredentialsSecretName, accessKeySecretKey),
								secretEnv("AWS_SECRET_ACCESS_KEY", instance.CredentialsSecretName, secretKeySecretKey),
								{Name: "AWS_DEFAULT_REGION", Value: config.Region},
							},
							Resources:       config.BootstrapResources,
							SecurityContext: restrictedContainerSecurityContext(),
						},
					},
				},
			},
		},
	}
	digest, err := bootstrapSpecHash(job.Spec)
	if err != nil {
		return nil, err
	}
	job.Annotations = map[string]string{bootstrapSpecHashAnnotation: digest}
	return job, nil
}

func bootstrapEndpoint(instance Instance) string {
	return fmt.Sprintf("http://%s:%d", instance.ServiceName, s3Port)
}

func bootstrapSpecHash(spec batchv1.JobSpec) (string, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("encode Evidence Store bootstrap Job spec: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", digest), nil
}
