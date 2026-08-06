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
	"sigs.k8s.io/yaml"
)

const workloadObservationFactsSchema = "chill.dacs.io/profiler-workload-observation.v1alpha2"

type WorkloadObservationResolverOptions struct {
	Namespace     string
	ConfigMapName string
	ConfigMapKey  string
}

type workloadObservationFacts struct {
	SchemaVersion string `json:"schemaVersion"`
	Image         string `json:"image"`
}

// KubernetesWorkloadObservationResolver reads the installation-owned helper
// image that the Preparer freezes into each new Run intent.
type KubernetesWorkloadObservationResolver struct {
	reader  client.Reader
	options WorkloadObservationResolverOptions
}

var _ profiler.WorkloadObservationResolver = (*KubernetesWorkloadObservationResolver)(nil)

func NewKubernetesWorkloadObservationResolver(
	reader client.Reader,
	options WorkloadObservationResolverOptions,
) (*KubernetesWorkloadObservationResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes workload-observation fact reader is required")
	}
	for name, value := range map[string]string{
		"fact-source namespace":    options.Namespace,
		"fact-source ConfigMap":    options.ConfigMapName,
		"workload-observation key": options.ConfigMapKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	return &KubernetesWorkloadObservationResolver{reader: reader, options: options}, nil
}

func (r *KubernetesWorkloadObservationResolver) ResolveWorkloadObservation(
	ctx context.Context,
) (profiler.ResolvedWorkloadObservation, error) {
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return profiler.ResolvedWorkloadObservation{}, fmt.Errorf(
			"get Profiler fact-source ConfigMap %s: %w", key, err,
		)
	}
	if configMap.UID == "" || configMap.ResourceVersion == "" {
		return profiler.ResolvedWorkloadObservation{}, errors.New(
			"Profiler fact-source ConfigMap has incomplete API identity",
		)
	}
	raw, exists := configMap.Data[r.options.ConfigMapKey]
	if !exists || strings.TrimSpace(raw) == "" {
		return profiler.ResolvedWorkloadObservation{}, fmt.Errorf(
			"Profiler fact-source ConfigMap missing key %q", r.options.ConfigMapKey,
		)
	}
	document, err := readSingleYAMLDocument([]byte(raw))
	if err != nil {
		return profiler.ResolvedWorkloadObservation{}, fmt.Errorf("decode workload-observation facts: %w", err)
	}
	var facts workloadObservationFacts
	if err := yaml.UnmarshalStrict(document, &facts); err != nil {
		return profiler.ResolvedWorkloadObservation{}, fmt.Errorf("decode workload-observation facts: %w", err)
	}
	if facts.SchemaVersion != workloadObservationFactsSchema {
		return profiler.ResolvedWorkloadObservation{}, fmt.Errorf(
			"unsupported workload-observation facts schema %q", facts.SchemaVersion,
		)
	}
	if strings.TrimSpace(facts.Image) == "" || strings.TrimSpace(facts.Image) != facts.Image {
		return profiler.ResolvedWorkloadObservation{}, errors.New("workload-observer image is required without surrounding whitespace")
	}
	return profiler.ResolvedWorkloadObservation{Image: facts.Image}, nil
}
