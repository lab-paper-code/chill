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
)

const (
	SourceKind                        = "edge-metrics"
	ShellyPowerMetric                 = "shelly_power_total_watts"
	ThermalObservationAvailableMetric = "device_thermal_throttle_observation_available"
	ThermalActiveMetric               = "device_thermal_throttle_active"
	ThermalCPUCoreEventsMetric        = "device_thermal_cpu_core_throttle_event_count"
	ThermalCPUPackageEventsMetric     = "device_thermal_cpu_package_throttle_event_count"
	ThermalLatchedMetric              = "device_thermal_throttle_latched"
	ThermalMaximumTemperatureMetric   = "device_thermal_max_celsius"
	maximumResponseBytes              = 1 << 20
)

var thermalMetricIdentity = metricobserver.ThermalMetricIdentity{
	ObservationAvailable:      ThermalObservationAvailableMetric,
	Active:                    ThermalActiveMetric,
	CPUCoreThrottleEvents:     ThermalCPUCoreEventsMetric,
	CPUPackageThrottleEvents:  ThermalCPUPackageEventsMetric,
	Latched:                   ThermalLatchedMetric,
	MaximumTemperatureCelsius: ThermalMaximumTemperatureMetric,
}

// Source reads one required wall-power value and the thermal facts exposed in
// the same edge-metrics response.
type Source struct {
	identity metricobserver.SourceIdentity
	client   *http.Client
}

// New returns an edge-metrics source for an already-resolved endpoint.
func New(identity metricobserver.SourceIdentity, client *http.Client) (*Source, error) {
	if identity.Kind != SourceKind {
		return nil, fmt.Errorf("unsupported edge-metrics source kind %q", identity.Kind)
	}
	if strings.TrimSpace(identity.Endpoint) == "" || strings.TrimSpace(identity.Endpoint) != identity.Endpoint {
		return nil, errors.New("endpoint is required without surrounding whitespace")
	}
	if identity.Metric != ShellyPowerMetric {
		return nil, fmt.Errorf("unsupported edge-metrics power metric %q", identity.Metric)
	}
	if identity.ThermalMetrics != nil {
		return nil, errors.New("thermal metric identity is supplied by the edge-metrics adapter")
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	metrics := thermalMetricIdentity
	identity.ThermalMetrics = &metrics
	return &Source{identity: identity, client: client}, nil
}

// Identity returns an owned copy of the source and typed metric identities.
func (s *Source) Identity() metricobserver.SourceIdentity {
	identity := s.identity
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
	return parseReading(bytes.NewReader(payload))
}

type rawMetric struct {
	found bool
	value string
	err   error
}

func parseReading(reader io.Reader) (metricobserver.Reading, error) {
	names := []string{
		ShellyPowerMetric,
		ThermalObservationAvailableMetric,
		ThermalActiveMetric,
		ThermalCPUCoreEventsMetric,
		ThermalCPUPackageEventsMetric,
		ThermalLatchedMetric,
		ThermalMaximumTemperatureMetric,
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

	watts, err := requiredNonNegative(metrics[ShellyPowerMetric], ShellyPowerMetric)
	if err != nil {
		return metricobserver.Reading{}, err
	}
	thermal := metricobserver.ThermalReading{}
	thermal.ObservationAvailable = thermalBool(
		metrics[ThermalObservationAvailableMetric], ThermalObservationAvailableMetric, true, &thermal,
	)
	thermal.Active = thermalBool(metrics[ThermalActiveMetric], ThermalActiveMetric, false, &thermal)
	thermal.Latched = thermalBool(metrics[ThermalLatchedMetric], ThermalLatchedMetric, false, &thermal)
	thermal.CPUCoreThrottleEvents = thermalCounter(
		metrics[ThermalCPUCoreEventsMetric], ThermalCPUCoreEventsMetric, &thermal,
	)
	thermal.CPUPackageThrottleEvents = thermalCounter(
		metrics[ThermalCPUPackageEventsMetric], ThermalCPUPackageEventsMetric, &thermal,
	)
	thermal.MaximumTemperatureCelsius = thermalTemperature(
		metrics[ThermalMaximumTemperatureMetric], ThermalMaximumTemperatureMetric, &thermal,
	)
	if thermal.ObservationAvailable != nil && *thermal.ObservationAvailable &&
		thermal.Active == nil && thermal.CPUCoreThrottleEvents == nil && thermal.CPUPackageThrottleEvents == nil &&
		len(thermal.Issues) == 0 {
		thermal.Issues = append(thermal.Issues, metricobserver.MetricIssue{
			Metric: ThermalObservationAvailableMetric, Reason: metricobserver.FailureReasonMetricMissing,
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
