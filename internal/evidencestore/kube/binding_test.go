package kube

import (
	"context"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/component"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBindingFromJobRecoversFrozenDeliveryContract(t *testing.T) {
	t.Parallel()
	job := bindingJob()
	binding, err := BindingFromJob(job, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if binding.Store != "chill-evidence" || binding.Endpoint != "http://store:9000" ||
		binding.Bucket != "profiling" || binding.Region != "us-east-1" ||
		binding.CredentialsSecretName != "store-credentials" {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestBindingFromJobRejectsAmbiguousOrInvalidContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*batchv1.Job)
		match  string
	}{
		{
			name: "runtime container missing",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.Containers[0].Name = "other"
			},
			match: "no runtime container",
		},
		{
			name: "duplicate store argument",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.Containers[0].Args = append(
					job.Spec.Template.Spec.Containers[0].Args, "--store-name=other",
				)
			},
			match: "binding store is required",
		},
		{
			name: "credentials use different Secrets",
			mutate: func(job *batchv1.Job) {
				job.Spec.Template.Spec.Containers[0].Env[1].ValueFrom.SecretKeyRef.Name = "other"
			},
			match: "invalid Evidence Store Secret binding",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			job := bindingJob()
			test.mutate(job)
			_, err := BindingFromJob(job, "runtime")
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("BindingFromJob() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestLoadCredentialsReadsOnlyContractKeys(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "store-credentials"},
		Data: map[string][]byte{
			component.EvidenceStoreAccessKeyKey: []byte("access"),
			component.EvidenceStoreSecretKeyKey: []byte("secret"),
			"unrelated":                         []byte("ignored"),
		},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	credentials, err := LoadCredentials(context.Background(), reader, secret.Namespace, secret.Name)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessKeyID != "access" || credentials.SecretAccessKey != "secret" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func bindingJob() *batchv1.Job {
	secretEnv := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "store-credentials"}, Key: key,
		}}}
	}
	return &batchv1.Job{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "runtime",
			Args: []string{
				"--store-name=chill-evidence", "--store-endpoint=http://store:9000",
				"--store-bucket=profiling", "--store-region=us-east-1",
			},
			Env: []corev1.EnvVar{
				secretEnv("AWS_ACCESS_KEY_ID", component.EvidenceStoreAccessKeyKey),
				secretEnv("AWS_SECRET_ACCESS_KEY", component.EvidenceStoreSecretKeyKey),
			},
		}},
	}}}}
}
