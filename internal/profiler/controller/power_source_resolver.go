package controller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/powerobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/profiler"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	powerSourcesSchema = "chill.dacs.io/profiler-power-sources.v1alpha1"
	edgeMetricsKind    = "edge-metrics"
)

var ErrPowerSourceUnavailable = errors.New("power source unavailable")

type PowerSourceResolverOptions struct {
	Namespace     string
	ConfigMapName string
	ConfigMapKey  string
}

type KubernetesPowerSourceResolver struct {
	reader  client.Reader
	options PowerSourceResolverOptions
}

var _ profiler.PowerSourceResolver = (*KubernetesPowerSourceResolver)(nil)

func NewKubernetesPowerSourceResolver(
	reader client.Reader,
	options PowerSourceResolverOptions,
) (*KubernetesPowerSourceResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes power-source reader is required")
	}
	for name, value := range map[string]string{
		"power-source namespace":      options.Namespace,
		"power-source ConfigMap name": options.ConfigMapName,
		"power-source ConfigMap key":  options.ConfigMapKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	return &KubernetesPowerSourceResolver{reader: reader, options: options}, nil
}

type powerSourcesDocument struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Sources       []powerSourceDeclaration `json:"sources"`
}

type powerSourceDeclaration struct {
	Node     string `json:"node"`
	Kind     string `json:"kind"`
	Endpoint string `json:"endpoint"`
	Metric   string `json:"metric"`
}

func (r *KubernetesPowerSourceResolver) ResolvePowerSource(
	ctx context.Context,
	nodeName string,
) (profiler.ResolvedPowerSource, error) {
	if strings.TrimSpace(nodeName) == "" || strings.TrimSpace(nodeName) != nodeName {
		return profiler.ResolvedPowerSource{}, errors.New("power-source Node name is required without surrounding whitespace")
	}
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return profiler.ResolvedPowerSource{}, fmt.Errorf("get Profiler power-source ConfigMap %s: %w", key, err)
	}
	if configMap.UID == "" || configMap.ResourceVersion == "" {
		return profiler.ResolvedPowerSource{}, errors.New("Profiler power-source ConfigMap has incomplete API identity")
	}
	raw, exists := configMap.Data[r.options.ConfigMapKey]
	if !exists || strings.TrimSpace(raw) == "" {
		return profiler.ResolvedPowerSource{}, fmt.Errorf(
			"Profiler power-source ConfigMap missing key %q", r.options.ConfigMapKey,
		)
	}
	documentBytes, err := readSingleYAMLDocument([]byte(raw))
	if err != nil {
		return profiler.ResolvedPowerSource{}, fmt.Errorf("decode Profiler power sources: %w", err)
	}
	var document powerSourcesDocument
	if err := yaml.UnmarshalStrict(documentBytes, &document); err != nil {
		return profiler.ResolvedPowerSource{}, fmt.Errorf("decode Profiler power sources: %w", err)
	}
	if document.SchemaVersion != powerSourcesSchema {
		return profiler.ResolvedPowerSource{}, fmt.Errorf("unsupported Profiler power-sources schema %q", document.SchemaVersion)
	}
	if len(document.Sources) == 0 {
		return profiler.ResolvedPowerSource{}, errors.New("Profiler power sources require at least one entry")
	}

	seen := make(map[string]struct{}, len(document.Sources))
	var selected *powerSourceDeclaration
	for index := range document.Sources {
		source := document.Sources[index]
		if err := validatePowerSourceDeclaration(source); err != nil {
			return profiler.ResolvedPowerSource{}, fmt.Errorf("validate power source %d: %w", index, err)
		}
		if _, duplicate := seen[source.Node]; duplicate {
			return profiler.ResolvedPowerSource{}, fmt.Errorf("duplicate power source for Node %q", source.Node)
		}
		seen[source.Node] = struct{}{}
		if source.Node == nodeName {
			copy := source
			selected = &copy
		}
	}
	if selected == nil {
		return profiler.ResolvedPowerSource{}, fmt.Errorf("%w: no entry for Node %q", ErrPowerSourceUnavailable, nodeName)
	}
	digest, err := canonicalDigest(*selected)
	if err != nil {
		return profiler.ResolvedPowerSource{}, fmt.Errorf("digest selected power source: %w", err)
	}
	return profiler.ResolvedPowerSource{
		Kind: selected.Kind, Endpoint: selected.Endpoint, Metric: selected.Metric,
		ConfigurationContentDigest: digest,
	}, nil
}

func validatePowerSourceDeclaration(source powerSourceDeclaration) error {
	for name, value := range map[string]string{
		"node": source.Node, "kind": source.Kind, "endpoint": source.Endpoint, "metric": source.Metric,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if source.Kind != edgeMetricsKind {
		return fmt.Errorf("unsupported power-source kind %q", source.Kind)
	}
	if source.Metric != edgemetrics.ShellyPowerMetric {
		return fmt.Errorf("unsupported edge-metrics metric %q", source.Metric)
	}
	endpoint, err := url.Parse(source.Endpoint)
	if err != nil {
		return fmt.Errorf("parse edge-metrics endpoint: %w", err)
	}
	if endpoint.Scheme != "http" || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.Path != "/metrics" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("edge-metrics endpoint must be an http URL ending at /metrics without credentials, query, or fragment")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("edge-metrics endpoint must contain an explicit valid port")
	}
	return nil
}
