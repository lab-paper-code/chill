package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const metricSourcesSchema = "chill.dacs.io/profiler-metric-sources.v1alpha6"

var ErrMetricSourceUnavailable = errors.New("metric source unavailable")

type MetricSourceResolverOptions struct {
	Namespace     string
	ConfigMapName string
	ConfigMapKey  string
}

type KubernetesMetricSourceResolver struct {
	reader  client.Reader
	options MetricSourceResolverOptions
}

var _ profiler.MetricSourceResolver = (*KubernetesMetricSourceResolver)(nil)

func NewKubernetesMetricSourceResolver(
	reader client.Reader,
	options MetricSourceResolverOptions,
) (*KubernetesMetricSourceResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes metric-source reader is required")
	}
	for name, value := range map[string]string{
		"metric-source namespace":      options.Namespace,
		"metric-source ConfigMap name": options.ConfigMapName,
		"metric-source ConfigMap key":  options.ConfigMapKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	return &KubernetesMetricSourceResolver{reader: reader, options: options}, nil
}

type metricSourcesDocument struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Sources       []MetricSourceDeclaration `json:"sources"`
}

// MetricSourceDeclaration is one Node's configured wall meter. MeterID names
// the physical meter; serving binds operation costs to it.
type MetricSourceDeclaration struct {
	Node            string                             `json:"node"`
	MeterID         string                             `json:"meterID"`
	Kind            string                             `json:"kind"`
	Endpoint        string                             `json:"endpoint"`
	PowerMetric     string                             `json:"powerMetric"`
	MetricLabels    map[string]string                  `json:"metricLabels"`
	ThermalEndpoint string                             `json:"thermalEndpoint"`
	ThermalMetrics  profilingrun.ThermalMetricContract `json:"thermalMetrics"`
}

func (r *KubernetesMetricSourceResolver) ResolveMetricSource(
	ctx context.Context,
	nodeName string,
) (profiler.ResolvedMetricSource, error) {
	if strings.TrimSpace(nodeName) == "" || strings.TrimSpace(nodeName) != nodeName {
		return profiler.ResolvedMetricSource{}, errors.New("metric-source Node name is required without surrounding whitespace")
	}
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return profiler.ResolvedMetricSource{}, fmt.Errorf("get Profiler metric-source ConfigMap %s: %w", key, err)
	}
	if configMap.UID == "" || configMap.ResourceVersion == "" {
		return profiler.ResolvedMetricSource{}, errors.New("Profiler metric-source ConfigMap has incomplete API identity")
	}
	raw, exists := configMap.Data[r.options.ConfigMapKey]
	if !exists || strings.TrimSpace(raw) == "" {
		return profiler.ResolvedMetricSource{}, fmt.Errorf(
			"Profiler metric-source ConfigMap missing key %q", r.options.ConfigMapKey,
		)
	}
	sources, err := DecodeMetricSources([]byte(raw))
	if err != nil {
		return profiler.ResolvedMetricSource{}, err
	}
	var selected *MetricSourceDeclaration
	for index := range sources {
		if sources[index].Node == nodeName {
			selected = &sources[index]
		}
	}
	if selected == nil {
		return profiler.ResolvedMetricSource{}, fmt.Errorf("%w: no entry for Node %q", ErrMetricSourceUnavailable, nodeName)
	}
	resolved := profiler.ResolvedMetricSource{
		Kind: selected.Kind, Endpoint: selected.Endpoint, PowerMetric: selected.PowerMetric,
		MetricLabels: maps.Clone(selected.MetricLabels), ThermalEndpoint: selected.ThermalEndpoint,
		ThermalMetrics: selected.ThermalMetrics,
	}
	return resolved, nil
}

// DecodeMetricSources returns the validated metric-source declarations, one
// per Node and one per meter.
func DecodeMetricSources(raw []byte) ([]MetricSourceDeclaration, error) {
	documentBytes, err := readSingleYAMLDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("decode Profiler metric sources: %w", err)
	}
	var document metricSourcesDocument
	if err := yaml.UnmarshalStrict(documentBytes, &document); err != nil {
		return nil, fmt.Errorf("decode Profiler metric sources: %w", err)
	}
	if document.SchemaVersion != metricSourcesSchema {
		return nil, fmt.Errorf("unsupported Profiler metric-sources schema %q", document.SchemaVersion)
	}
	if len(document.Sources) == 0 {
		return nil, errors.New("Profiler metric sources require at least one entry")
	}
	nodes := make(map[string]struct{}, len(document.Sources))
	meters := make(map[string]struct{}, len(document.Sources))
	for index, source := range document.Sources {
		if err := validateMetricSourceDeclaration(source); err != nil {
			return nil, fmt.Errorf("validate metric source %d: %w", index, err)
		}
		if _, duplicate := nodes[source.Node]; duplicate {
			return nil, fmt.Errorf("duplicate metric source for Node %q", source.Node)
		}
		if _, duplicate := meters[source.MeterID]; duplicate {
			return nil, fmt.Errorf("duplicate metric source meterID %q", source.MeterID)
		}
		nodes[source.Node], meters[source.MeterID] = struct{}{}, struct{}{}
	}
	return document.Sources, nil
}

func validateMetricSourceDeclaration(source MetricSourceDeclaration) error {
	for name, value := range map[string]string{
		"node": source.Node, "meterID": source.MeterID, "kind": source.Kind, "endpoint": source.Endpoint,
		"powerMetric": source.PowerMetric,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if source.Kind != edgemetrics.SourceKind {
		return fmt.Errorf("unsupported metric-source kind %q", source.Kind)
	}
	metricContract := profilingrun.MetricContract{
		PowerMetric: source.PowerMetric, MetricLabels: source.MetricLabels,
		ThermalEndpoint: source.ThermalEndpoint, ThermalMetrics: source.ThermalMetrics,
	}
	if err := metricContract.ValidateMetricIdentities(); err != nil {
		return fmt.Errorf("validate metric identities: %w", err)
	}
	if err := validateFilteredPowerEndpoint(source.Endpoint, source.MetricLabels); err != nil {
		return err
	}
	if err := validateMetricEndpoint(source.ThermalEndpoint); err != nil {
		return fmt.Errorf("validate thermal endpoint: %w", err)
	}
	return nil
}

func validateMetricEndpoint(raw string) error {
	endpoint, err := parseMetricEndpoint(raw)
	if err != nil {
		return err
	}
	if endpoint.RawQuery != "" || endpoint.ForceQuery {
		return errors.New("edge-metrics endpoint must not contain a query")
	}
	return nil
}

func validateFilteredPowerEndpoint(raw string, labels map[string]string) error {
	endpoint, err := parseMetricEndpoint(raw)
	if err != nil {
		return err
	}
	query, err := url.ParseQuery(endpoint.RawQuery)
	if err != nil {
		return fmt.Errorf("parse power endpoint query: %w", err)
	}
	hostnames := query["hostname"]
	if len(query) != 1 || len(hostnames) != 1 {
		return errors.New("power endpoint must contain exactly one hostname query value")
	}
	hostname := hostnames[0]
	if strings.TrimSpace(hostname) == "" || strings.TrimSpace(hostname) != hostname {
		return errors.New("power endpoint hostname query value is required without surrounding whitespace")
	}
	if labels["hostname"] != hostname {
		return errors.New("power endpoint hostname query value must match metricLabels.hostname")
	}
	return nil
}

func parseMetricEndpoint(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) != raw {
		return nil, errors.New("endpoint is required without surrounding whitespace")
	}
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse edge-metrics endpoint: %w", err)
	}
	if endpoint.Scheme != "http" || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.Fragment != "" {
		return nil, errors.New("edge-metrics endpoint must be an http URL without credentials or fragment")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("edge-metrics endpoint must contain an explicit valid port")
	}
	if endpoint.Path == "" || endpoint.RawPath != "" || path.Clean(endpoint.Path) != endpoint.Path {
		return nil, errors.New("edge-metrics endpoint must contain a clean nonempty path")
	}
	return endpoint, nil
}
