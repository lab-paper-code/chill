// Package kube adapts Kubernetes workload and Secret objects to Evidence Store
// connection material. It does not read or interpret evidence objects.
package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/component"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Binding contains the non-secret connection values frozen into one
// measurement Job and the name of its credentials Secret.
type Binding struct {
	Store                 string
	Endpoint              string
	Bucket                string
	Region                string
	CredentialsSecretName string
}

func (b Binding) Validate() error {
	for name, value := range map[string]string{
		"store": b.Store, "endpoint": b.Endpoint, "bucket": b.Bucket,
		"region": b.Region, "credentials Secret": b.CredentialsSecretName,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("Evidence Store binding %s is required without surrounding whitespace", name)
		}
	}
	return nil
}

// BindingFromJob recovers the delivery contract frozen into the runtime
// container. It intentionally does not resolve the current ChillSystem config.
func BindingFromJob(job *batchv1.Job, runtimeContainerName string) (Binding, error) {
	if job == nil {
		return Binding{}, errors.New("measurement Job is required")
	}
	runtimeContainer, ok := containerNamed(job.Spec.Template.Spec.Containers, runtimeContainerName)
	if !ok {
		return Binding{}, errors.New("existing measurement Job has no runtime container")
	}
	binding := Binding{
		Store:    argumentValue(runtimeContainer.Args, "--store-name="),
		Endpoint: argumentValue(runtimeContainer.Args, "--store-endpoint="),
		Bucket:   argumentValue(runtimeContainer.Args, "--store-bucket="),
		Region:   argumentValue(runtimeContainer.Args, "--store-region="),
	}
	accessSecret, accessKey := secretReference(runtimeContainer.Env, "AWS_ACCESS_KEY_ID")
	secretSecret, secretKey := secretReference(runtimeContainer.Env, "AWS_SECRET_ACCESS_KEY")
	if accessSecret == "" || accessSecret != secretSecret ||
		accessKey != component.EvidenceStoreAccessKeyKey || secretKey != component.EvidenceStoreSecretKeyKey {
		return Binding{}, errors.New("existing measurement Job has an invalid Evidence Store Secret binding")
	}
	binding.CredentialsSecretName = accessSecret
	if err := binding.Validate(); err != nil {
		return Binding{}, fmt.Errorf("validate existing measurement Job delivery binding: %w", err)
	}
	return binding, nil
}

type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// LoadCredentials reads only the two keys named by CHILL's Evidence Store
// contract. Callers must not return these values through an operator API.
func LoadCredentials(
	ctx context.Context,
	reader client.Reader,
	namespace, name string,
) (Credentials, error) {
	if reader == nil {
		return Credentials{}, errors.New("Kubernetes Secret reader is required")
	}
	secret := &corev1.Secret{}
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return Credentials{}, fmt.Errorf("get retained Evidence Store credentials Secret: %w", err)
	}
	credentials := Credentials{
		AccessKeyID:     string(secret.Data[component.EvidenceStoreAccessKeyKey]),
		SecretAccessKey: string(secret.Data[component.EvidenceStoreSecretKeyKey]),
	}
	if strings.TrimSpace(credentials.AccessKeyID) == "" || strings.TrimSpace(credentials.SecretAccessKey) == "" {
		return Credentials{}, errors.New("retained Evidence Store credentials are incomplete")
	}
	return credentials, nil
}

func containerNamed(containers []corev1.Container, name string) (corev1.Container, bool) {
	for _, container := range containers {
		if container.Name == name {
			return container, true
		}
	}
	return corev1.Container{}, false
}

func argumentValue(arguments []string, prefix string) string {
	value := ""
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			if value != "" {
				return ""
			}
			value = strings.TrimPrefix(argument, prefix)
		}
	}
	return value
}

func secretReference(env []corev1.EnvVar, name string) (string, string) {
	for _, variable := range env {
		if variable.Name == name && variable.ValueFrom != nil && variable.ValueFrom.SecretKeyRef != nil {
			return variable.ValueFrom.SecretKeyRef.Name, variable.ValueFrom.SecretKeyRef.Key
		}
	}
	return "", ""
}
