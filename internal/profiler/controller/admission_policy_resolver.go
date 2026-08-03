package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AdmissionPolicyResolverOptions struct {
	Namespace     string
	ConfigMapName string
	ConfigMapKey  string
}

// KubernetesAdmissionPolicyResolver reads only installation-time trusted
// policy configuration. IntentFreezer copies its selected output into the
// immutable Run record before any Job is created.
type KubernetesAdmissionPolicyResolver struct {
	reader  client.Reader
	options AdmissionPolicyResolverOptions
}

func NewKubernetesAdmissionPolicyResolver(
	reader client.Reader,
	options AdmissionPolicyResolverOptions,
) (*KubernetesAdmissionPolicyResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes API reader is required")
	}
	for name, value := range map[string]string{
		"namespace": options.Namespace, "ConfigMap name": options.ConfigMapName, "ConfigMap key": options.ConfigMapKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("admission-policy %s is required without surrounding whitespace", name)
		}
	}
	return &KubernetesAdmissionPolicyResolver{reader: reader, options: options}, nil
}

func (r *KubernetesAdmissionPolicyResolver) Resolve(
	ctx context.Context,
	name string,
) (profileradmission.FrozenPolicy, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return profileradmission.FrozenPolicy{}, errors.New("admission-policy name is required without surrounding whitespace")
	}
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return profileradmission.FrozenPolicy{}, fmt.Errorf("get admission-policy ConfigMap %s: %w", key, err)
	}
	if configMap.UID == "" || configMap.ResourceVersion == "" {
		return profileradmission.FrozenPolicy{}, errors.New("admission-policy ConfigMap has incomplete Kubernetes identity")
	}
	if _, exists := configMap.BinaryData[r.options.ConfigMapKey]; exists {
		return profileradmission.FrozenPolicy{}, errors.New("admission-policy ConfigMap key must be stored in text data")
	}
	raw, exists := configMap.Data[r.options.ConfigMapKey]
	if !exists || raw == "" {
		return profileradmission.FrozenPolicy{}, fmt.Errorf("admission-policy ConfigMap %s is missing key %q", key, r.options.ConfigMapKey)
	}
	return profileradmission.FreezeConfiguredPolicy([]byte(raw), name, profileradmission.PolicySource{
		Kind: profileradmission.ConfigMapPolicySourceKind, Namespace: configMap.Namespace, Name: configMap.Name,
		UID: string(configMap.UID), ResourceVersion: configMap.ResourceVersion, Key: r.options.ConfigMapKey,
	})
}
