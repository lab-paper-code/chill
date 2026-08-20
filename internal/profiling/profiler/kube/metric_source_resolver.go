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
	prommodel "github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const metricSourcesSchema = "chill.dacs.io/profiler-metric-sources.v1alpha4"

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
	Sources       []metricSourceDeclaration `json:"sources"`
}

type metricSourceDeclaration struct {
	Node            string                              `json:"node"`
	Kind            string                              `json:"kind"`
	Endpoint        string                              `json:"endpoint"`
	PowerMetric     string                              `json:"powerMetric"`
	ThermalMetrics  profilingrun.ThermalMetricContract  `json:"thermalMetrics"`
	TransitionPower *transitionPowerSelectorDeclaration `json:"transitionPower,omitempty"`
}

type transitionPowerSelectorDeclaration struct {
	Endpoint    string            `json:"endpoint"`
	PowerMetric string            `json:"powerMetric"`
	Labels      map[string]string `json:"labels"`
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
	documentBytes, err := readSingleYAMLDocument([]byte(raw))
	if err != nil {
		return profiler.ResolvedMetricSource{}, fmt.Errorf("decode Profiler metric sources: %w", err)
	}
	var document metricSourcesDocument
	if err := yaml.UnmarshalStrict(documentBytes, &document); err != nil {
		return profiler.ResolvedMetricSource{}, fmt.Errorf("decode Profiler metric sources: %w", err)
	}
	if document.SchemaVersion != metricSourcesSchema {
		return profiler.ResolvedMetricSource{}, fmt.Errorf("unsupported Profiler metric-sources schema %q", document.SchemaVersion)
	}
	if len(document.Sources) == 0 {
		return profiler.ResolvedMetricSource{}, errors.New("Profiler metric sources require at least one entry")
	}

	seen := make(map[string]struct{}, len(document.Sources))
	var selected *metricSourceDeclaration
	for index := range document.Sources {
		source := document.Sources[index]
		if err := validateMetricSourceDeclaration(source); err != nil {
			return profiler.ResolvedMetricSource{}, fmt.Errorf("validate metric source %d: %w", index, err)
		}
		if _, duplicate := seen[source.Node]; duplicate {
			return profiler.ResolvedMetricSource{}, fmt.Errorf("duplicate metric source for Node %q", source.Node)
		}
		seen[source.Node] = struct{}{}
		if source.Node == nodeName {
			copy := source
			selected = &copy
		}
	}
	if selected == nil {
		return profiler.ResolvedMetricSource{}, fmt.Errorf("%w: no entry for Node %q", ErrMetricSourceUnavailable, nodeName)
	}
	resolved := profiler.ResolvedMetricSource{
		Kind: selected.Kind, Endpoint: selected.Endpoint, PowerMetric: selected.PowerMetric,
		ThermalMetrics: selected.ThermalMetrics,
	}
	if selected.TransitionPower != nil {
		resolved.TransitionPower = &profiler.ResolvedTransitionPower{
			Endpoint: selected.TransitionPower.Endpoint, PowerMetric: selected.TransitionPower.PowerMetric,
			Labels: maps.Clone(selected.TransitionPower.Labels),
		}
	}
	return resolved, nil
}

func validateMetricSourceDeclaration(source metricSourceDeclaration) error {
	for name, value := range map[string]string{
		"node": source.Node, "kind": source.Kind, "endpoint": source.Endpoint, "powerMetric": source.PowerMetric,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if source.Kind != edgemetrics.SourceKind {
		return fmt.Errorf("unsupported metric-source kind %q", source.Kind)
	}
	metricContract := profilingrun.MetricContract{
		PowerMetric: source.PowerMetric, ThermalMetrics: source.ThermalMetrics,
	}
	if err := metricContract.ValidateMetricIdentities(); err != nil {
		return fmt.Errorf("validate metric identities: %w", err)
	}
	if err := validateMetricEndpoint(source.Endpoint); err != nil {
		return err
	}
	if source.TransitionPower != nil {
		if err := validateTransitionPowerSelector(*source.TransitionPower); err != nil {
			return fmt.Errorf("validate transition power: %w", err)
		}
	}
	return nil
}

func validateTransitionPowerSelector(selector transitionPowerSelectorDeclaration) error {
	if strings.TrimSpace(selector.PowerMetric) == "" || strings.TrimSpace(selector.PowerMetric) != selector.PowerMetric {
		return errors.New("powerMetric is required without surrounding whitespace")
	}
	if !prommodel.IsValidMetricName(prommodel.LabelValue(selector.PowerMetric)) {
		return errors.New("powerMetric must be a valid Prometheus metric name")
	}
	if len(selector.Labels) == 0 {
		return errors.New("exact labels are required")
	}
	for name, value := range selector.Labels {
		if !prommodel.LabelName(name).IsValid() {
			return fmt.Errorf("label name %q is invalid", name)
		}
		if !prommodel.LabelValue(value).IsValid() {
			return fmt.Errorf("label %q value is invalid UTF-8", name)
		}
	}
	return validateTransitionPowerEndpoint(selector.Endpoint, selector.Labels)
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

func validateTransitionPowerEndpoint(raw string, labels map[string]string) error {
	endpoint, err := parseMetricEndpoint(raw)
	if err != nil {
		return err
	}
	query, err := url.ParseQuery(endpoint.RawQuery)
	if err != nil {
		return fmt.Errorf("parse transition-power endpoint query: %w", err)
	}
	hostnames := query["hostname"]
	if len(query) != 1 || len(hostnames) != 1 {
		return errors.New("transition-power endpoint must contain exactly one hostname query value")
	}
	hostname := hostnames[0]
	if strings.TrimSpace(hostname) == "" || strings.TrimSpace(hostname) != hostname {
		return errors.New("transition-power endpoint hostname query value is required without surrounding whitespace")
	}
	if labels["hostname"] != hostname {
		return errors.New("transition-power endpoint hostname query value must match labels.hostname")
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
