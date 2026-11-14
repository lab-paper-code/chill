package kube

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ExecutionPolicyResolverOptions struct {
	Namespace                 string
	ConfigMapName             string
	ConfigMapKey              string
	MeasurementMemoryFraction float64
}

// KubernetesExecutionPolicyResolver selects installation-time worker envelopes
// and freezes the runtime memory cap before the actor/curve recipe is recorded.
type KubernetesExecutionPolicyResolver struct {
	reader  client.Reader
	options ExecutionPolicyResolverOptions
	session bool
}

func NewKubernetesExecutionPolicyResolver(
	reader client.Reader,
	options ExecutionPolicyResolverOptions,
) (*KubernetesExecutionPolicyResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes API reader is required")
	}
	if math.IsNaN(options.MeasurementMemoryFraction) || options.MeasurementMemoryFraction <= 0 || options.MeasurementMemoryFraction >= 1 {
		return nil, errors.New("measurement memory fraction must be between 0 and 1 (exclusive)")
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

// NewKubernetesSessionExecutionPolicyResolver omits unused worker prerequisites.
func NewKubernetesSessionExecutionPolicyResolver(reader client.Reader, options ExecutionPolicyResolverOptions) (*KubernetesExecutionPolicyResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes API reader is required")
	}
	return &KubernetesExecutionPolicyResolver{reader: reader, options: options, session: true}, nil
}

func (r *KubernetesExecutionPolicyResolver) Resolve(
	ctx context.Context,
	config profilingrun.RunConfig,
) (profiler.FrozenExecutionPolicy, error) {
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return profiler.FrozenExecutionPolicy{}, fmt.Errorf("get execution-policy ConfigMap %s: %w", key, err)
	}
	raw := []byte(configMap.Data[r.options.ConfigMapKey])
	if len(raw) == 0 {
		raw = configMap.BinaryData[r.options.ConfigMapKey]
	}
	if len(raw) == 0 {
		return profiler.FrozenExecutionPolicy{}, profiler.InvalidExecutionPolicy(
			"execution-policy ConfigMap %s is missing key %q",
			key,
			r.options.ConfigMapKey,
		)
	}
	freeze := profiler.FreezeConfiguredExecutionPolicy
	if r.session {
		freeze = profiler.FreezeSessionExecutionPolicy
	}
	frozen, err := freeze(raw, config, profiler.ExecutionPolicySource{
		Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: configMap.Namespace, Name: configMap.Name,
		UID: string(configMap.UID), ResourceVersion: configMap.ResourceVersion, Key: r.options.ConfigMapKey,
	})
	if err != nil {
		return profiler.FrozenExecutionPolicy{}, profiler.NewTerminalResolutionError(profilingrun.ResolutionReasonExecutionPolicyInvalid, err)
	}
	memory := config.TargetNode.AllocatableMemoryBytes
	if !r.session {
		node := &corev1.Node{}
		if err := r.reader.Get(ctx, types.NamespacedName{Name: config.TargetNode.Name}, node); err != nil {
			return profiler.FrozenExecutionPolicy{}, fmt.Errorf("get measurement Node memory: %w", err)
		}
		if string(node.UID) != config.TargetNode.UID {
			return profiler.FrozenExecutionPolicy{}, errors.New("measurement Node UID changed since resolution")
		}
		allocatable := node.Status.Allocatable[corev1.ResourceMemory]
		memory = allocatable.Value()
	}
	if memory <= 0 {
		return profiler.FrozenExecutionPolicy{}, errors.New("measurement Node has no positive allocatable memory")
	}
	limit := int64(float64(memory) * r.options.MeasurementMemoryFraction)
	frozen.Policy.RuntimeMemoryLimitBytes = min(frozen.Policy.RuntimeMemoryLimitBytes, limit)
	if frozen.Policy.RuntimeMemoryLimitBytes < frozen.Policy.RuntimeMemoryRequestBytes {
		return profiler.FrozenExecutionPolicy{}, errors.New("measurement Node memory limit is below the runtime memory request")
	}
	return frozen, nil
}
