package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ExecutionPolicyResolverOptions struct {
	Namespace     string
	ConfigMapName string
	ConfigMapKey  string
}

// KubernetesExecutionPolicyResolver reads installation-time worker envelopes.
// IntentFreezer copies the selected envelope and source identity into the
// immutable Run record before execution is materialized.
type KubernetesExecutionPolicyResolver struct {
	reader  client.Reader
	options ExecutionPolicyResolverOptions
}

func NewKubernetesExecutionPolicyResolver(
	reader client.Reader,
	options ExecutionPolicyResolverOptions,
) (*KubernetesExecutionPolicyResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes API reader is required")
	}
	for name, value := range map[string]string{
		"namespace":      options.Namespace,
		"ConfigMap name": options.ConfigMapName,
		"ConfigMap key":  options.ConfigMapKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("execution-policy %s is required without surrounding whitespace", name)
		}
	}
	return &KubernetesExecutionPolicyResolver{reader: reader, options: options}, nil
}

func (r *KubernetesExecutionPolicyResolver) Resolve(
	ctx context.Context,
	intent profilingrun.Intent,
) (profiler.FrozenExecutionPolicy, error) {
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return profiler.FrozenExecutionPolicy{}, fmt.Errorf("get execution-policy ConfigMap %s: %w", key, err)
	}
	if configMap.UID == "" || configMap.ResourceVersion == "" {
		return profiler.FrozenExecutionPolicy{}, profiler.InvalidExecutionPolicy(
			"execution-policy ConfigMap has incomplete Kubernetes identity",
		)
	}
	if _, exists := configMap.BinaryData[r.options.ConfigMapKey]; exists {
		return profiler.FrozenExecutionPolicy{}, profiler.InvalidExecutionPolicy(
			"execution-policy ConfigMap key must be stored in text data",
		)
	}
	raw, exists := configMap.Data[r.options.ConfigMapKey]
	if !exists || raw == "" {
		return profiler.FrozenExecutionPolicy{}, profiler.InvalidExecutionPolicy(
			"execution-policy ConfigMap %s is missing key %q",
			key,
			r.options.ConfigMapKey,
		)
	}
	frozen, err := profiler.FreezeConfiguredExecutionPolicy([]byte(raw), intent, profiler.ExecutionPolicySource{
		Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: configMap.Namespace, Name: configMap.Name,
		UID: string(configMap.UID), ResourceVersion: configMap.ResourceVersion, Key: r.options.ConfigMapKey,
	})
	if err != nil {
		return profiler.FrozenExecutionPolicy{}, profiler.NewTerminalFreezeError(profiler.FreezeReasonExecutionPolicyInvalid, err)
	}
	return frozen, nil
}
