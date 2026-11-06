package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
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
// image that the Preparer freezes into each new RunConfig.
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

// ResolveMeasurementWorkload reads the actual Workload, never an assumed p95.
func (r *KubernetesWorkloadObservationResolver) ResolveMeasurementWorkload(
	ctx context.Context, name string, selection profiler.CandidateSelection,
) (profilingrun.MeasurementContract, error) {
	workload := &edgev1alpha1.Workload{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: name}, workload); err != nil {
		return profilingrun.MeasurementContract{}, fmt.Errorf("get measurement Workload %q: %w", name, err)
	}
	if workload.UID == "" ||
		workload.Spec.ModelRef.Name != selection.ModelSpec ||
		workload.Spec.PreprocessorRef.Name != selection.Preprocessor ||
		workload.Spec.DatasetRef == nil || workload.Spec.DatasetRef.Name != selection.Dataset ||
		workload.Spec.SLO.TargetLatency.Duration <= 0 {
		return profilingrun.MeasurementContract{}, errors.New("measurement Workload identity, model, preprocessor, dataset or SLO differs from the selected measurement")
	}
	minimum, err := profilingrun.MinimumServiceSamples(workload.Spec.SLO.Percentile)
	if err != nil {
		return profilingrun.MeasurementContract{}, err
	}
	return profilingrun.MeasurementContract{
		WorkloadName: workload.Name, WorkloadUID: string(workload.UID), WorkloadResourceVersion: workload.ResourceVersion,
		SLOPercentile: workload.Spec.SLO.Percentile, SLOTargetLatencyNanos: workload.Spec.SLO.TargetLatency.Duration.Nanoseconds(),
		MinServiceSamples: minimum,
	}, nil
}
