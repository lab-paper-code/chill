package edgemetrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
)

func TestNewValidatesDependenciesAndFreezesTypedIdentity(t *testing.T) {
	if _, err := New(metricobserver.SourceIdentity{}, http.DefaultClient); err == nil {
		t.Fatal("New() error = nil, want endpoint validation error")
	}
	if _, err := New(metricobserver.SourceIdentity{Endpoint: "http://example.test/metrics"}, nil); err == nil {
		t.Fatal("New() error = nil, want client validation error")
	}
	source, err := New(metricobserver.SourceIdentity{
		NodeName: "jetsonx", Namespace: "monitoring", PodName: "exporter", Endpoint: " http://example.test/metrics ",
	}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	identity := source.Identity()
	if identity.Kind != "edge-metrics" || identity.Metric != ShellyPowerMetric ||
		identity.Endpoint != "http://example.test/metrics" || identity.ThermalMetrics == nil ||
		identity.ThermalMetrics.Active != ThermalActiveMetric {
		t.Fatalf("identity = %#v", identity)
	}
	identity.ThermalMetrics.Active = "mutated"
	if source.Identity().ThermalMetrics.Active != ThermalActiveMetric {
		t.Fatal("source identity retained a caller-owned pointer")
	}
}

func TestParseReadingPreservesAlignedDirectThermalFacts(t *testing.T) {
	reading, err := parseReading(strings.NewReader(`
shelly_power_total_watts{node="jetsonx"} 6.5 1234
device_thermal_throttle_observation_available 1
device_thermal_throttle_active 0
device_thermal_throttle_latched 1
device_thermal_max_celsius 52.125
`))
	if err != nil {
		t.Fatal(err)
	}
	thermal := reading.Thermal
	if reading.Watts != 6.5 || thermal.ObservationAvailable == nil || !*thermal.ObservationAvailable ||
		thermal.Active == nil || *thermal.Active || thermal.Latched == nil || !*thermal.Latched ||
		thermal.MaximumTemperatureCelsius == nil || *thermal.MaximumTemperatureCelsius != 52.125 ||
		len(thermal.Issues) != 0 {
		t.Fatalf("reading = %#v", reading)
	}
}

func TestParseReadingPreservesCPUCounterFacts(t *testing.T) {
	reading, err := parseReading(strings.NewReader(`
shelly_power_total_watts 4.2
device_thermal_throttle_observation_available 1
device_thermal_cpu_core_throttle_event_count 3.0
device_thermal_cpu_package_throttle_event_count 7
device_thermal_max_celsius 40
`))
	if err != nil {
		t.Fatal(err)
	}
	thermal := reading.Thermal
	if thermal.Active != nil || thermal.CPUCoreThrottleEvents == nil || *thermal.CPUCoreThrottleEvents != 3 ||
		thermal.CPUPackageThrottleEvents == nil || *thermal.CPUPackageThrottleEvents != 7 ||
		len(thermal.Issues) != 0 {
		t.Fatalf("thermal reading = %#v", thermal)
	}
}

func TestParseReadingKeepsThermalProblemsSeparateFromRequiredPower(t *testing.T) {
	tests := []struct {
		name    string
		thermal string
		metric  string
	}{
		{name: "availability missing", thermal: "", metric: ThermalObservationAvailableMetric},
		{name: "availability invalid", thermal: ThermalObservationAvailableMetric + " 2\n", metric: ThermalObservationAvailableMetric},
		{name: "signal missing", thermal: ThermalObservationAvailableMetric + " 1\n", metric: ThermalObservationAvailableMetric},
		{name: "counter invalid", thermal: ThermalObservationAvailableMetric + " 1\n" + ThermalCPUCoreEventsMetric + " 1.5\n", metric: ThermalCPUCoreEventsMetric},
		{name: "duplicate active", thermal: ThermalObservationAvailableMetric + " 1\n" + ThermalActiveMetric + " 0\n" + ThermalActiveMetric + " 0\n", metric: ThermalActiveMetric},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reading, err := parseReading(strings.NewReader(ShellyPowerMetric + " 4.2\n" + test.thermal))
			if err != nil {
				t.Fatal(err)
			}
			if reading.Watts != 4.2 || len(reading.Thermal.Issues) != 1 ||
				reading.Thermal.Issues[0].Metric != test.metric {
				t.Fatalf("reading = %#v", reading)
			}
		})
	}
}

func TestParseReadingFailsWhenRequiredPowerIsUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantError error
	}{
		{name: "missing", payload: "other_metric 1\n", wantError: metricobserver.ErrMetricMissing},
		{name: "duplicate", payload: ShellyPowerMetric + " 1\n" + ShellyPowerMetric + " 2\n", wantError: metricobserver.ErrInvalidReading},
		{name: "not a number", payload: ShellyPowerMetric + " nope\n", wantError: metricobserver.ErrInvalidReading},
		{name: "infinity", payload: ShellyPowerMetric + " +Inf\n", wantError: metricobserver.ErrInvalidReading},
		{name: "negative", payload: ShellyPowerMetric + " -1\n", wantError: metricobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseReading(strings.NewReader(test.payload))
			if !errors.Is(err, test.wantError) {
				t.Fatalf("parseReading() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestSourceReadMetrics(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		payload   string
		want      float64
		wantError error
	}{
		{name: "success", status: http.StatusOK, payload: ShellyPowerMetric + " 4.2\n", want: 4.2},
		{name: "HTTP failure", status: http.StatusInternalServerError, payload: "failed", wantError: errors.New("HTTP failure")},
		{name: "missing power", status: http.StatusOK, payload: "other_metric 1\n", wantError: metricobserver.ErrMetricMissing},
		{name: "oversized response", status: http.StatusOK, payload: strings.Repeat("x", maximumResponseBytes+1), wantError: metricobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.payload))
			}))
			defer server.Close()
			source, err := New(metricobserver.SourceIdentity{Endpoint: server.URL}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			reading, err := source.ReadMetrics(context.Background())
			if test.wantError != nil && err == nil {
				t.Fatalf("ReadMetrics() error = nil, want %v", test.wantError)
			}
			if errors.Is(test.wantError, metricobserver.ErrMetricMissing) && !errors.Is(err, metricobserver.ErrMetricMissing) {
				t.Fatalf("ReadMetrics() error = %v, want metric missing", err)
			}
			if errors.Is(test.wantError, metricobserver.ErrInvalidReading) && !errors.Is(err, metricobserver.ErrInvalidReading) {
				t.Fatalf("ReadMetrics() error = %v, want invalid reading", err)
			}
			if test.wantError == nil && (err != nil || reading.Watts != test.want) {
				t.Fatalf("ReadMetrics() = %#v, %v; want %v, nil", reading, err, test.want)
			}
		})
	}
}

func TestSourceReadMetricsHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	source, err := New(metricobserver.SourceIdentity{Endpoint: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = source.ReadMetrics(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReadMetrics() error = %v, want deadline exceeded", err)
	}
}

func TestSourceReadMetricsRejectsMalformedEndpoint(t *testing.T) {
	source, err := New(metricobserver.SourceIdentity{Endpoint: "://bad"}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ReadMetrics(context.Background())
	if err == nil || !strings.Contains(err.Error(), "build edge-metrics request") {
		t.Fatalf("ReadMetrics() error = %v, want request construction error", err)
	}
}
