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

func TestNewValidatesAndRetainsFrozenMetricIdentity(t *testing.T) {
	unsupportedKind := testSourceIdentity("http://example.test/metrics")
	unsupportedKind.Kind = "prometheus"
	missingPower := testSourceIdentity("http://example.test/metrics")
	missingPower.Metric = ""
	missingThermal := testSourceIdentity("http://example.test/metrics")
	missingThermal.ThermalMetrics = nil
	incompleteThermal := testSourceIdentity("http://example.test/metrics")
	incompleteThermal.ThermalMetrics.Active = ""
	duplicateMetric := testSourceIdentity("http://example.test/metrics")
	duplicateMetric.ThermalMetrics.Active = duplicateMetric.Metric

	tests := []struct {
		name     string
		identity metricobserver.SourceIdentity
		client   *http.Client
		want     string
	}{
		{name: "missing endpoint", identity: testSourceIdentity(""), client: http.DefaultClient, want: "endpoint is required"},
		{name: "endpoint whitespace", identity: testSourceIdentity(" http://example.test/metrics "), client: http.DefaultClient, want: "without surrounding whitespace"},
		{name: "unsupported kind", identity: unsupportedKind, client: http.DefaultClient, want: "unsupported edge-metrics source kind"},
		{name: "missing power metric", identity: missingPower, client: http.DefaultClient, want: "power metric identity is required"},
		{name: "missing thermal identity", identity: missingThermal, client: http.DefaultClient, want: "thermal metric identity is required"},
		{name: "incomplete thermal identity", identity: incompleteThermal, client: http.DefaultClient, want: "thermal active metric identity is required"},
		{name: "duplicate metric identity", identity: duplicateMetric, client: http.DefaultClient, want: "is duplicated"},
		{name: "missing client", identity: testSourceIdentity("http://example.test/metrics"), want: "HTTP client is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.identity, test.client); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want containing %q", err, test.want)
			}
		})
	}

	identity := testSourceIdentity("http://example.test/metrics")
	identity.NodeName = "target-node"
	identity.Namespace = "monitoring"
	identity.PodName = "exporter"
	expectedThermal := *identity.ThermalMetrics
	source, err := New(identity, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	identity.ThermalMetrics.Active = "caller_mutation"
	got := source.Identity()
	if got.Kind != identity.Kind || got.NodeName != identity.NodeName || got.Namespace != identity.Namespace ||
		got.PodName != identity.PodName || got.Metric != identity.Metric || got.Endpoint != identity.Endpoint ||
		got.ThermalMetrics == nil || *got.ThermalMetrics != expectedThermal {
		t.Fatalf("identity = %#v", got)
	}
	got.ThermalMetrics.Active = "returned_mutation"
	if source.Identity().ThermalMetrics.Active != expectedThermal.Active {
		t.Fatal("source identity retained a caller-owned thermal identity pointer")
	}
}

func testSourceIdentity(endpoint string) metricobserver.SourceIdentity {
	return metricobserver.SourceIdentity{
		Kind: SourceKind, Endpoint: endpoint, Metric: "configured_wall_power_watts",
		ThermalMetrics: &metricobserver.ThermalMetricIdentity{
			ObservationAvailable:      "configured_thermal_observation_available",
			Active:                    "configured_thermal_active",
			CPUCoreThrottleEvents:     "configured_thermal_cpu_core_events_total",
			CPUPackageThrottleEvents:  "configured_thermal_cpu_package_events_total",
			Latched:                   "configured_thermal_latched",
			MaximumTemperatureCelsius: "configured_thermal_max_celsius",
		},
	}
}

func TestParseReadingPreservesAlignedDirectThermalFacts(t *testing.T) {
	identity := testSourceIdentity("")
	metrics := *identity.ThermalMetrics
	reading, err := parseReading(strings.NewReader(strings.Join([]string{
		identity.Metric + `{node="target-node"} 6.5 1234`,
		metrics.ObservationAvailable + " 1",
		metrics.Active + " 0",
		metrics.Latched + " 1",
		metrics.MaximumTemperatureCelsius + " 52.125",
	}, "\n")+"\n"), identity.Metric, metrics)
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
	identity := testSourceIdentity("")
	metrics := *identity.ThermalMetrics
	reading, err := parseReading(strings.NewReader(strings.Join([]string{
		identity.Metric + " 4.2",
		metrics.ObservationAvailable + " 1",
		metrics.CPUCoreThrottleEvents + " 3.0",
		metrics.CPUPackageThrottleEvents + " 7",
		metrics.MaximumTemperatureCelsius + " 40",
	}, "\n")+"\n"), identity.Metric, metrics)
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
	identity := testSourceIdentity("")
	metrics := *identity.ThermalMetrics
	tests := []struct {
		name    string
		thermal string
		metric  string
	}{
		{name: "availability missing", thermal: "", metric: metrics.ObservationAvailable},
		{name: "availability invalid", thermal: metrics.ObservationAvailable + " 2\n", metric: metrics.ObservationAvailable},
		{name: "signal missing", thermal: metrics.ObservationAvailable + " 1\n", metric: metrics.ObservationAvailable},
		{name: "counter invalid", thermal: metrics.ObservationAvailable + " 1\n" + metrics.CPUCoreThrottleEvents + " 1.5\n", metric: metrics.CPUCoreThrottleEvents},
		{name: "duplicate active", thermal: metrics.ObservationAvailable + " 1\n" + metrics.Active + " 0\n" + metrics.Active + " 0\n", metric: metrics.Active},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reading, err := parseReading(
				strings.NewReader(identity.Metric+" 4.2\n"+test.thermal), identity.Metric, metrics,
			)
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
	identity := testSourceIdentity("")
	metrics := *identity.ThermalMetrics
	tests := []struct {
		name      string
		payload   string
		wantError error
	}{
		{name: "missing", payload: "other_metric 1\n", wantError: metricobserver.ErrMetricMissing},
		{name: "duplicate", payload: identity.Metric + " 1\n" + identity.Metric + " 2\n", wantError: metricobserver.ErrInvalidReading},
		{name: "not a number", payload: identity.Metric + " nope\n", wantError: metricobserver.ErrInvalidReading},
		{name: "infinity", payload: identity.Metric + " +Inf\n", wantError: metricobserver.ErrInvalidReading},
		{name: "negative", payload: identity.Metric + " -1\n", wantError: metricobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseReading(strings.NewReader(test.payload), identity.Metric, metrics)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("parseReading() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestSourceReadMetrics(t *testing.T) {
	identity := testSourceIdentity("")
	tests := []struct {
		name      string
		status    int
		payload   string
		want      float64
		wantError error
	}{
		{name: "success", status: http.StatusOK, payload: identity.Metric + " 4.2\n", want: 4.2},
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
			source, err := New(testSourceIdentity(server.URL), server.Client())
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
	source, err := New(testSourceIdentity(server.URL), server.Client())
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
	source, err := New(testSourceIdentity("://bad"), http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ReadMetrics(context.Background())
	if err == nil || !strings.Contains(err.Error(), "build edge-metrics request") {
		t.Fatalf("ReadMetrics() error = %v, want request construction error", err)
	}
}
