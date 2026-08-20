// Package edgemetrics adapts one edge-metrics Prometheus endpoint to a typed
// CHILL metricobserver.Source.
package edgemetrics

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	prommodel "github.com/prometheus/common/model"
)

const (
	SourceKind           = "edge-metrics"
	maximumResponseBytes = 1 << 20
)

// Source reads one configured wall-power source, with either aligned thermal
// facts or one exact power-series selector.
type Source struct {
	identity metricobserver.SourceIdentity
	client   *http.Client
}

// New returns an edge-metrics source for an already-resolved endpoint.
func New(identity metricobserver.SourceIdentity, client *http.Client) (*Source, error) {
	if err := validateSourceLocation(identity); err != nil {
		return nil, err
	}
	if identity.ThermalMetrics == nil {
		return nil, errors.New("thermal metric identity is required")
	}
	if identity.MetricLabels != nil {
		return nil, errors.New("exact power label selector is not supported by the aligned power and thermal source")
	}
	if err := validateMetricIdentity(identity.Metric, *identity.ThermalMetrics); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	metrics := *identity.ThermalMetrics
	identity.ThermalMetrics = &metrics
	return &Source{identity: identity, client: client}, nil
}

// NewPowerSeries returns a power-only source selecting exactly one configured
// Prometheus series from an already-resolved edge-metrics endpoint.
func NewPowerSeries(identity metricobserver.SourceIdentity, client *http.Client) (*Source, error) {
	if err := validateSourceLocation(identity); err != nil {
		return nil, err
	}
	if strings.TrimSpace(identity.Metric) == "" || strings.TrimSpace(identity.Metric) != identity.Metric {
		return nil, errors.New("power metric identity is required without surrounding whitespace")
	}
	if identity.ThermalMetrics != nil {
		return nil, errors.New("thermal metric identity is not supported by the exact power-series source")
	}
	if err := validateExactPowerLabels(identity.MetricLabels); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	identity.MetricLabels = cloneMetricLabels(identity.MetricLabels)
	return &Source{identity: identity, client: client}, nil
}

func validateSourceLocation(identity metricobserver.SourceIdentity) error {
	if identity.Kind != SourceKind {
		return fmt.Errorf("unsupported edge-metrics source kind %q", identity.Kind)
	}
	if strings.TrimSpace(identity.Endpoint) == "" || strings.TrimSpace(identity.Endpoint) != identity.Endpoint {
		return errors.New("endpoint is required without surrounding whitespace")
	}
	return nil
}

func validateExactPowerLabels(labels map[string]string) error {
	if len(labels) == 0 {
		return errors.New("exact power label selector is required")
	}
	for name, value := range labels {
		if !prommodel.LabelName(name).IsValid() {
			return fmt.Errorf("exact power label name %q is invalid", name)
		}
		if !prommodel.LabelValue(value).IsValid() {
			return fmt.Errorf("exact power label %q value is invalid UTF-8", name)
		}
	}
	return nil
}

func cloneMetricLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for name, value := range labels {
		cloned[name] = value
	}
	return cloned
}

func validateMetricIdentity(power string, thermal metricobserver.ThermalMetricIdentity) error {
	metrics := []struct {
		name  string
		value string
	}{
		{name: "power", value: power},
		{name: "thermal observation-available", value: thermal.ObservationAvailable},
		{name: "thermal active", value: thermal.Active},
		{name: "thermal CPU-core-events", value: thermal.CPUCoreThrottleEvents},
		{name: "thermal CPU-package-events", value: thermal.CPUPackageThrottleEvents},
		{name: "thermal latched", value: thermal.Latched},
		{name: "thermal maximum-temperature", value: thermal.MaximumTemperatureCelsius},
	}
	seen := make(map[string]struct{}, len(metrics))
	for _, metric := range metrics {
		if strings.TrimSpace(metric.value) == "" || strings.TrimSpace(metric.value) != metric.value {
			return fmt.Errorf("%s metric identity is required without surrounding whitespace", metric.name)
		}
		if _, duplicate := seen[metric.value]; duplicate {
			return fmt.Errorf("metric identity %q is duplicated", metric.value)
		}
		seen[metric.value] = struct{}{}
	}
	return nil
}

// Identity returns an owned copy of the source and typed metric identities.
func (s *Source) Identity() metricobserver.SourceIdentity {
	identity := s.identity
	identity.MetricLabels = cloneMetricLabels(s.identity.MetricLabels)
	if s.identity.ThermalMetrics != nil {
		metrics := *s.identity.ThermalMetrics
		identity.ThermalMetrics = &metrics
	}
	return identity
}

// ReadMetrics returns one aligned wall-power and thermal reading.
func (s *Source) ReadMetrics(ctx context.Context) (metricobserver.Reading, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.identity.Endpoint, nil)
	if err != nil {
		return metricobserver.Reading{}, fmt.Errorf("build edge-metrics request: %w", err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return metricobserver.Reading{}, fmt.Errorf("read edge-metrics: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return metricobserver.Reading{}, fmt.Errorf("read edge-metrics: HTTP %d", response.StatusCode)
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return metricobserver.Reading{}, fmt.Errorf("read edge-metrics response: %w", err)
	}
	if len(payload) > maximumResponseBytes {
		return metricobserver.Reading{}, fmt.Errorf(
			"%w: edge-metrics response exceeds %d bytes", metricobserver.ErrInvalidReading, maximumResponseBytes,
		)
	}
	if len(s.identity.MetricLabels) != 0 {
		return parsePowerSeries(bytes.NewReader(payload), s.identity.Metric, s.identity.MetricLabels)
	}
	return parseReading(bytes.NewReader(payload), s.identity.Metric, *s.identity.ThermalMetrics)
}

func parsePowerSeries(
	reader io.Reader,
	metricName string,
	labels map[string]string,
) (metricobserver.Reading, error) {
	parser := expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(reader)
	if err != nil {
		return metricobserver.Reading{}, fmt.Errorf("%w: parse Prometheus exposition: %v", metricobserver.ErrInvalidReading, err)
	}
	family, found := families[metricName]
	if !found {
		return metricobserver.Reading{}, fmt.Errorf("%w: %s", metricobserver.ErrMetricMissing, metricName)
	}
	if family.GetType() != dto.MetricType_GAUGE {
		return metricobserver.Reading{}, fmt.Errorf(
			"%w: %s is %s, want GAUGE", metricobserver.ErrInvalidReading, metricName, family.GetType(),
		)
	}

	var match *dto.Metric
	for _, metric := range family.GetMetric() {
		if !hasExactLabels(metric, labels) {
			continue
		}
		if match != nil {
			return metricobserver.Reading{}, fmt.Errorf(
				"%w: multiple %s series match the exact label selector", metricobserver.ErrInvalidReading, metricName,
			)
		}
		match = metric
	}
	if match == nil {
		return metricobserver.Reading{}, fmt.Errorf(
			"%w: %s with the exact label selector", metricobserver.ErrMetricMissing, metricName,
		)
	}
	if match.Gauge == nil {
		return metricobserver.Reading{}, fmt.Errorf("%w: %s gauge value is absent", metricobserver.ErrInvalidReading, metricName)
	}
	watts := match.GetGauge().GetValue()
	if math.IsNaN(watts) || math.IsInf(watts, 0) || watts < 0 {
		return metricobserver.Reading{}, fmt.Errorf("%w: %s power value is not finite and non-negative", metricobserver.ErrInvalidReading, metricName)
	}
	return metricobserver.Reading{Watts: watts}, nil
}

func hasExactLabels(metric *dto.Metric, expected map[string]string) bool {
	if len(metric.GetLabel()) != len(expected) {
		return false
	}
	for _, label := range metric.GetLabel() {
		value, found := expected[label.GetName()]
		if !found || value != label.GetValue() {
			return false
		}
	}
	return true
}

type rawMetric struct {
	found bool
	value string
	err   error
}

func parseReading(
	reader io.Reader,
	powerMetric string,
	thermalMetrics metricobserver.ThermalMetricIdentity,
) (metricobserver.Reading, error) {
	names := []string{
		powerMetric,
		thermalMetrics.ObservationAvailable,
		thermalMetrics.Active,
		thermalMetrics.CPUCoreThrottleEvents,
		thermalMetrics.CPUPackageThrottleEvents,
		thermalMetrics.Latched,
		thermalMetrics.MaximumTemperatureCelsius,
	}
	metrics := make(map[string]*rawMetric, len(names))
	for _, name := range names {
		metrics[name] = &rawMetric{}
	}

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if brace := strings.IndexByte(name, '{'); brace >= 0 {
			name = name[:brace]
		}
		metric, wanted := metrics[name]
		if !wanted {
			continue
		}
		if metric.found {
			metric.err = fmt.Errorf("multiple %s series", name)
			continue
		}
		metric.found = true
		if len(fields) > 3 {
			metric.err = fmt.Errorf("malformed %s series", name)
			continue
		}
		metric.value = fields[1]
	}
	if err := scanner.Err(); err != nil {
		return metricobserver.Reading{}, fmt.Errorf("parse edge-metrics response: %w", err)
	}

	watts, err := requiredNonNegative(metrics[powerMetric], powerMetric)
	if err != nil {
		return metricobserver.Reading{}, err
	}
	thermal := metricobserver.ThermalReading{}
	thermal.ObservationAvailable = thermalBool(
		metrics[thermalMetrics.ObservationAvailable], thermalMetrics.ObservationAvailable, true, &thermal,
	)
	thermal.Active = thermalBool(metrics[thermalMetrics.Active], thermalMetrics.Active, false, &thermal)
	thermal.Latched = thermalBool(metrics[thermalMetrics.Latched], thermalMetrics.Latched, false, &thermal)
	thermal.CPUCoreThrottleEvents = thermalCounter(
		metrics[thermalMetrics.CPUCoreThrottleEvents], thermalMetrics.CPUCoreThrottleEvents, &thermal,
	)
	thermal.CPUPackageThrottleEvents = thermalCounter(
		metrics[thermalMetrics.CPUPackageThrottleEvents], thermalMetrics.CPUPackageThrottleEvents, &thermal,
	)
	thermal.MaximumTemperatureCelsius = thermalTemperature(
		metrics[thermalMetrics.MaximumTemperatureCelsius], thermalMetrics.MaximumTemperatureCelsius, &thermal,
	)
	if thermal.ObservationAvailable != nil && *thermal.ObservationAvailable &&
		thermal.Active == nil && thermal.CPUCoreThrottleEvents == nil && thermal.CPUPackageThrottleEvents == nil &&
		len(thermal.Issues) == 0 {
		thermal.Issues = append(thermal.Issues, metricobserver.MetricIssue{
			Metric: thermalMetrics.ObservationAvailable, Reason: metricobserver.FailureReasonMetricMissing,
			Message: "thermal observation is available but no direct or cumulative throttling signal is exposed",
		})
	}
	return metricobserver.Reading{Watts: watts, Thermal: thermal}, nil
}

func requiredNonNegative(metric *rawMetric, name string) (float64, error) {
	if !metric.found {
		return 0, fmt.Errorf("%w: %s", metricobserver.ErrMetricMissing, name)
	}
	if metric.err != nil {
		return 0, fmt.Errorf("%w: %v", metricobserver.ErrInvalidReading, metric.err)
	}
	value, err := strconv.ParseFloat(metric.value, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("%w: %s=%q", metricobserver.ErrInvalidReading, name, metric.value)
	}
	return value, nil
}

func thermalBool(
	metric *rawMetric,
	name string,
	required bool,
	reading *metricobserver.ThermalReading,
) *bool {
	if !metric.found {
		if required {
			addThermalIssue(reading, name, metricobserver.FailureReasonMetricMissing, "metric is missing")
		}
		return nil
	}
	if metric.err != nil {
		addThermalIssue(reading, name, metricobserver.FailureReasonInvalidReading, metric.err.Error())
		return nil
	}
	value, err := strconv.ParseFloat(metric.value, 64)
	if err != nil || (value != 0 && value != 1) {
		addThermalIssue(reading, name, metricobserver.FailureReasonInvalidReading, "boolean gauge must be 0 or 1")
		return nil
	}
	result := value == 1
	return &result
}

func thermalCounter(
	metric *rawMetric,
	name string,
	reading *metricobserver.ThermalReading,
) *uint64 {
	if !metric.found {
		return nil
	}
	if metric.err != nil {
		addThermalIssue(reading, name, metricobserver.FailureReasonInvalidReading, metric.err.Error())
		return nil
	}
	value, err := strconv.ParseFloat(metric.value, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 ||
		math.Trunc(value) != value || value > math.MaxUint64 {
		addThermalIssue(reading, name, metricobserver.FailureReasonInvalidReading, "counter must be a non-negative integer")
		return nil
	}
	result := uint64(value)
	return &result
}

func thermalTemperature(
	metric *rawMetric,
	name string,
	reading *metricobserver.ThermalReading,
) *float64 {
	if !metric.found {
		return nil
	}
	if metric.err != nil {
		addThermalIssue(reading, name, metricobserver.FailureReasonInvalidReading, metric.err.Error())
		return nil
	}
	value, err := strconv.ParseFloat(metric.value, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		addThermalIssue(reading, name, metricobserver.FailureReasonInvalidReading, "temperature must be finite")
		return nil
	}
	return &value
}

func addThermalIssue(
	reading *metricobserver.ThermalReading,
	metric string,
	reason metricobserver.FailureReason,
	message string,
) {
	reading.Issues = append(reading.Issues, metricobserver.MetricIssue{
		Metric: metric, Reason: reason, Message: message,
	})
}
