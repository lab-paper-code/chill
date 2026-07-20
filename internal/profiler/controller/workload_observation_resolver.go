package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lab-paper-code/chill/internal/profiler"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const workloadObservationFactsSchema = "chill.dacs.io/profiler-workload-observation.v1alpha1"

type WorkloadObservationResolverOptions struct {
	Namespace     string
	ConfigMapName string
	ConfigMapKey  string
}

type workloadObservationFacts struct {
	SchemaVersion     string                               `json:"schemaVersion"`
	Image             string                               `json:"image"`
	AllowedBackground profilingrun.AllowedBackgroundPolicy `json:"allowedBackground"`
}

// KubernetesWorkloadObservationResolver reads the installation-owned helper
// image and policy that the Preparer freezes into each new Run intent.
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
	if !pinnedWorkloadImage.MatchString(facts.Image) {
		return profiler.ResolvedWorkloadObservation{}, errors.New("workload-observer image must be digest-pinned")
	}
	sort.Slice(facts.AllowedBackground.Selectors, func(left, right int) bool {
		leftJSON, _ := json.Marshal(facts.AllowedBackground.Selectors[left])
		rightJSON, _ := json.Marshal(facts.AllowedBackground.Selectors[right])
		return string(leftJSON) < string(rightJSON)
	})
	if err := facts.AllowedBackground.Validate(); err != nil {
		return profiler.ResolvedWorkloadObservation{}, fmt.Errorf("validate allowed-background policy: %w", err)
	}
	return profiler.ResolvedWorkloadObservation{
		Image: facts.Image, Policy: facts.AllowedBackground,
	}, nil
}
