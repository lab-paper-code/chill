package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/profiler"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ActuationPolicyResolverOptions struct {
	Namespace     string
	ConfigMapName string
	ConfigMapKey  string
}

// KubernetesActuationPolicyResolver reads installation-time Node actuation
// authorization and timing policy. The selected canonical policy is copied
// into the immutable Run record before its required state is confirmed.
type KubernetesActuationPolicyResolver struct {
	reader  client.Reader
	options ActuationPolicyResolverOptions
}

var _ profiler.ActuationPolicyResolver = (*KubernetesActuationPolicyResolver)(nil)

func NewKubernetesActuationPolicyResolver(
	reader client.Reader,
	options ActuationPolicyResolverOptions,
) (*KubernetesActuationPolicyResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes API reader is required")
	}
	for name, value := range map[string]string{
		"namespace":      options.Namespace,
		"ConfigMap name": options.ConfigMapName,
		"ConfigMap key":  options.ConfigMapKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("actuation-policy %s is required without surrounding whitespace", name)
		}
	}
	return &KubernetesActuationPolicyResolver{reader: reader, options: options}, nil
}

func (r *KubernetesActuationPolicyResolver) ResolveActuationPolicy(
	ctx context.Context,
	selection profiler.ActuationSelection,
) (profiler.FrozenActuationPolicy, error) {
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return profiler.FrozenActuationPolicy{}, fmt.Errorf("get actuation-policy ConfigMap %s: %w", key, err)
	}
	if configMap.UID == "" || configMap.ResourceVersion == "" {
		return profiler.FrozenActuationPolicy{}, invalidActuationPolicy(
			"actuation-policy ConfigMap has incomplete Kubernetes identity",
		)
	}
	if _, exists := configMap.BinaryData[r.options.ConfigMapKey]; exists {
		return profiler.FrozenActuationPolicy{}, invalidActuationPolicy(
			"actuation-policy ConfigMap key must be stored in text data",
		)
	}
	raw, exists := configMap.Data[r.options.ConfigMapKey]
	if !exists || raw == "" {
		return profiler.FrozenActuationPolicy{}, invalidActuationPolicy(
			"actuation-policy ConfigMap %s is missing key %q",
			key,
			r.options.ConfigMapKey,
		)
	}
	frozen, err := profiler.FreezeConfiguredActuationPolicy(
		[]byte(raw),
		selection,
		profiler.ActuationPolicySource{
			Kind: profiler.ConfigMapActuationPolicySourceKind, Namespace: configMap.Namespace,
			Name: configMap.Name, UID: string(configMap.UID), ResourceVersion: configMap.ResourceVersion,
			Key: r.options.ConfigMapKey,
		},
	)
	if err != nil {
		return profiler.FrozenActuationPolicy{}, newTerminalFreezeError(freezeReasonActuationPolicyInvalid, err)
	}
	return frozen, nil
}
