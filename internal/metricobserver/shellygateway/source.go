// Package shellygateway adapts the central Shelly Gateway Prometheus endpoint
// to one target-specific CHILL metricobserver.Source.
package shellygateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const (
	PowerMetric         = "shelly_power_total_watts"
	maximumResponseSize = 1 << 20
)

// SeriesIdentity identifies one target's series at the central gateway.
type SeriesIdentity struct {
	DeviceType string
	Hostname   string
}

// Source reads one target's wall-power series from a central Shelly Gateway.
type Source struct {
	identity metricobserver.SourceIdentity
	series   SeriesIdentity
	client   *http.Client
}

var _ metricobserver.Source = (*Source)(nil)

// New returns a source for one exact target series at an already-resolved
// central gateway endpoint.
func New(
	identity metricobserver.SourceIdentity,
	series SeriesIdentity,
	client *http.Client,
) (*Source, error) {
	if err := validateExactValue("target node name", identity.NodeName); err != nil {
		return nil, err
	}
	if err := validateEndpoint(identity.Endpoint); err != nil {
		return nil, err
	}
	if err := validateExactValue("device type", series.DeviceType); err != nil {
		return nil, err
	}
	if err := validateExactValue("hostname", series.Hostname); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}

	identity.Kind = "shelly-gateway"
	identity.Metric = canonicalSelector(series)
	identity.ThermalMetrics = nil
	return &Source{identity: identity, series: series, client: client}, nil
}

func validateExactValue(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have surrounding whitespace", name)
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return errors.New("endpoint is required")
	}
	if strings.TrimSpace(endpoint) != endpoint {
		return errors.New("endpoint must not have surrounding whitespace")
	}
	if strings.Contains(endpoint, "#") {
		return errors.New("endpoint fragment is not allowed")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse endpoint: %w", err)
	}
	if parsed.Scheme != "http" {
		return errors.New("endpoint scheme must be http")
	}
	if parsed.User != nil {
		return errors.New("endpoint credentials are not allowed")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host == "" || port == "" {
		return errors.New("endpoint must have an explicit host and port")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return errors.New("endpoint must have a valid explicit port")
	}
	if parsed.Path != "/prometheus" || parsed.RawPath != "" {
		return errors.New("endpoint path must be exactly /prometheus")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("endpoint query is not allowed")
	}
	return nil
}

func canonicalSelector(series SeriesIdentity) string {
	return PowerMetric + `{device_type=` + strconv.Quote(series.DeviceType) +
		`,hostname=` + strconv.Quote(series.Hostname) + `}`
}

// Identity returns an owned copy of the factual source identity.
func (s *Source) Identity() metricobserver.SourceIdentity {
	return s.identity
}

// ReadMetrics returns the one exact target series from the central response.
func (s *Source) ReadMetrics(ctx context.Context) (metricobserver.Reading, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.identity.Endpoint, nil)
	if err != nil {
		return metricobserver.Reading{}, fmt.Errorf("build Shelly Gateway request: %w", err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return metricobserver.Reading{}, fmt.Errorf("read Shelly Gateway: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return metricobserver.Reading{}, fmt.Errorf("read Shelly Gateway: HTTP %d", response.StatusCode)
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseSize+1))
	if err != nil {
		return metricobserver.Reading{}, fmt.Errorf("read Shelly Gateway response: %w", err)
	}
	if len(payload) > maximumResponseSize {
		return metricobserver.Reading{}, fmt.Errorf(
			"%w: Shelly Gateway response exceeds %d bytes",
			metricobserver.ErrInvalidReading,
			maximumResponseSize,
		)
	}
	return parseReading(bytes.NewReader(payload), s.series)
}

func parseReading(reader io.Reader, series SeriesIdentity) (metricobserver.Reading, error) {
	parser := expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(reader)
	if err != nil {
		return metricobserver.Reading{}, fmt.Errorf("%w: parse Shelly Gateway response: %v", metricobserver.ErrInvalidReading, err)
	}
	family, found := families[PowerMetric]
	if !found {
		return metricobserver.Reading{}, fmt.Errorf("%w: %s", metricobserver.ErrMetricMissing, PowerMetric)
	}
	if family.GetType() != dto.MetricType_GAUGE {
		return metricobserver.Reading{}, fmt.Errorf(
			"%w: %s must be a gauge",
			metricobserver.ErrInvalidReading,
			PowerMetric,
		)
	}

	var matched *dto.Metric
	for _, metric := range family.GetMetric() {
		if !hasExactLabels(metric.GetLabel(), series) {
			continue
		}
		if matched != nil {
			return metricobserver.Reading{}, fmt.Errorf(
				"%w: multiple exact %s series",
				metricobserver.ErrInvalidReading,
				PowerMetric,
			)
		}
		matched = metric
	}
	if matched == nil {
		return metricobserver.Reading{}, fmt.Errorf(
			"%w: %s",
			metricobserver.ErrMetricMissing,
			canonicalSelector(series),
		)
	}
	if matched.Gauge == nil {
		return metricobserver.Reading{}, fmt.Errorf("%w: exact series has no gauge value", metricobserver.ErrInvalidReading)
	}
	watts := matched.GetGauge().GetValue()
	if math.IsNaN(watts) || math.IsInf(watts, 0) || watts < 0 {
		return metricobserver.Reading{}, fmt.Errorf(
			"%w: %s has unusable value %v",
			metricobserver.ErrInvalidReading,
			canonicalSelector(series),
			watts,
		)
	}
	return metricobserver.Reading{Watts: watts}, nil
}

func hasExactLabels(labels []*dto.LabelPair, series SeriesIdentity) bool {
	if len(labels) != 2 {
		return false
	}
	values := map[string]string{
		"device_type": series.DeviceType,
		"hostname":    series.Hostname,
	}
	for _, label := range labels {
		if label == nil {
			return false
		}
		value, found := values[label.GetName()]
		if !found || label.GetValue() != value {
			return false
		}
		delete(values, label.GetName())
	}
	return len(values) == 0
}
