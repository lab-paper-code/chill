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

func TestNewRequiresCompositeIdentityAndOwnsIt(t *testing.T) {
	valid := testCompositeIdentity("http://power.test/metrics", "http://thermal.test/metrics")
	tests := []struct {
		name   string
		mutate func(*metricobserver.SourceIdentity)
		client *http.Client
		want   string
	}{
		{name: "kind", mutate: func(identity *metricobserver.SourceIdentity) { identity.Kind = "other" }, client: http.DefaultClient, want: "unsupported"},
		{name: "power endpoint", mutate: func(identity *metricobserver.SourceIdentity) { identity.Endpoint = "" }, client: http.DefaultClient, want: "endpoint is required"},
		{name: "power metric", mutate: func(identity *metricobserver.SourceIdentity) { identity.Metric = "" }, client: http.DefaultClient, want: "power metric identity"},
		{name: "labels", mutate: func(identity *metricobserver.SourceIdentity) { identity.MetricLabels = nil }, client: http.DefaultClient, want: "exact power label selector"},
		{name: "thermal endpoint", mutate: func(identity *metricobserver.SourceIdentity) { identity.ThermalEndpoint = "" }, client: http.DefaultClient, want: "thermal endpoint"},
		{name: "thermal identity", mutate: func(identity *metricobserver.SourceIdentity) { identity.ThermalMetrics = nil }, client: http.DefaultClient, want: "thermal metric identity"},
		{name: "semantic overlap", mutate: func(identity *metricobserver.SourceIdentity) { identity.ThermalMetrics.Active = identity.Metric }, client: http.DefaultClient, want: "duplicated"},
		{name: "client", mutate: func(*metricobserver.SourceIdentity) {}, want: "HTTP client"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			labels := cloneMetricLabels(valid.MetricLabels)
			thermal := *valid.ThermalMetrics
			candidate.MetricLabels = labels
			candidate.ThermalMetrics = &thermal
			test.mutate(&candidate)
			if _, err := New(candidate, test.client); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want containing %q", err, test.want)
			}
		})
	}

	source, err := New(valid, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	valid.MetricLabels["hostname"] = callerMutation
	valid.ThermalMetrics.Active = callerMutation
	first := source.Identity()
	first.MetricLabels["hostname"] = "result-mutation"
	first.ThermalMetrics.Active = "result-mutation"
	second := source.Identity()
	if second.MetricLabels["hostname"] != selectedNodeLabel || second.ThermalMetrics.Active != "thermal_active" {
		t.Fatalf("source identity aliases caller or returned values: %#v", second)
	}
}

func TestSourceReadMetricsCombinesExactPowerAndTypedThermal(t *testing.T) {
	power := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("hostname") != selectedNodeLabel {
			t.Errorf("power query = %q", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte("# TYPE wall_power_watts gauge\nwall_power_watts{hostname=\"node-7\",device_type=\"test_device\"} 5.2\n"))
	}))
	defer power.Close()
	thermal := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "" {
			t.Errorf("thermal query = %q", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte(strings.Join([]string{
			"thermal_observation_available 1",
			"thermal_active 0",
			"thermal_core_events_total 4",
			"thermal_package_events_total 2",
			"thermal_latched 1",
			"thermal_max_celsius 47.5",
		}, "\n") + "\n"))
	}))
	defer thermal.Close()

	identity := testCompositeIdentity(power.URL+"/prometheus?hostname=node-7", thermal.URL+"/metrics")
	source, err := New(identity, power.Client())
	if err != nil {
		t.Fatal(err)
	}
	reading, err := source.ReadMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reading.Watts != 5.2 || reading.Thermal.ObservationAvailable == nil ||
		!*reading.Thermal.ObservationAvailable || reading.Thermal.Active == nil || *reading.Thermal.Active ||
		reading.Thermal.CPUCoreThrottleEvents == nil || *reading.Thermal.CPUCoreThrottleEvents != 4 ||
		reading.Thermal.CPUPackageThrottleEvents == nil || *reading.Thermal.CPUPackageThrottleEvents != 2 ||
		reading.Thermal.Latched == nil || !*reading.Thermal.Latched ||
		reading.Thermal.MaximumTemperatureCelsius == nil || *reading.Thermal.MaximumTemperatureCelsius != 47.5 {
		t.Fatalf("ReadMetrics() = %#v", reading)
	}
}

func TestSourceReadMetricsFailsWhenEitherEndpointFailsOrIsOversized(t *testing.T) {
	tests := []struct {
		name          string
		powerStatus   int
		thermalStatus int
		powerBody     string
		thermalBody   string
		want          string
	}{
		{name: "power status", powerStatus: http.StatusBadGateway, thermalStatus: http.StatusOK, want: "read power endpoint"},
		{name: "thermal status", powerStatus: http.StatusOK, thermalStatus: http.StatusServiceUnavailable, powerBody: validPowerBody(), want: "read thermal endpoint"},
		{name: "power oversized", powerStatus: http.StatusOK, thermalStatus: http.StatusOK, powerBody: strings.Repeat("x", maximumResponseBytes+1), want: "read power endpoint"},
		{name: "thermal oversized", powerStatus: http.StatusOK, thermalStatus: http.StatusOK, powerBody: validPowerBody(), thermalBody: strings.Repeat("x", maximumResponseBytes+1), want: "read thermal endpoint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			power := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.powerStatus)
				_, _ = response.Write([]byte(test.powerBody))
			}))
			defer power.Close()
			thermal := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.thermalStatus)
				_, _ = response.Write([]byte(test.thermalBody))
			}))
			defer thermal.Close()
			source, err := New(testCompositeIdentity(
				power.URL+"/prometheus?hostname=node-7", thermal.URL+"/metrics",
			), power.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.ReadMetrics(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadMetrics() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestSourceReadMetricsUsesOneContextForBothEndpoints(t *testing.T) {
	for _, blocked := range []string{"power", "thermal"} {
		t.Run(blocked, func(t *testing.T) {
			power := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if blocked == "power" {
					<-request.Context().Done()
					return
				}
				_, _ = response.Write([]byte(validPowerBody()))
			}))
			defer power.Close()
			thermal := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				<-request.Context().Done()
			}))
			defer thermal.Close()
			source, err := New(testCompositeIdentity(
				power.URL+"/prometheus?hostname=node-7", thermal.URL+"/metrics",
			), power.Client())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_, err = source.ReadMetrics(ctx)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ReadMetrics() error = %v, want deadline exceeded", err)
			}
		})
	}
}

func TestParseThermalReadingRetainsTypedIssues(t *testing.T) {
	thermal, err := parseThermalReading(strings.NewReader("thermal_observation_available 1\nthermal_active invalid\n"), testThermalIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if thermal.ObservationAvailable == nil || !*thermal.ObservationAvailable || thermal.Active != nil || len(thermal.Issues) != 1 {
		t.Fatalf("parseThermalReading() = %#v", thermal)
	}
}

func TestParseThermalReadingPreservesMissingAndInvalidFacts(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		metric string
		reason metricobserver.FailureReason
	}{
		{
			name: "availability missing", body: "thermal_active 0\n",
			metric: "thermal_observation_available", reason: metricobserver.FailureReasonMetricMissing,
		},
		{
			name: "availability invalid", body: "thermal_observation_available 2\nthermal_active 0\n",
			metric: "thermal_observation_available", reason: metricobserver.FailureReasonInvalidReading,
		},
		{
			name: "available without throttling signal", body: "thermal_observation_available 1\n",
			metric: "thermal_observation_available", reason: metricobserver.FailureReasonMetricMissing,
		},
		{
			name: "invalid counter", body: "thermal_observation_available 1\nthermal_active 0\nthermal_core_events_total -1\n",
			metric: "thermal_core_events_total", reason: metricobserver.FailureReasonInvalidReading,
		},
		{
			name: "duplicate active", body: "thermal_observation_available 1\nthermal_active 0\nthermal_active 1\n",
			metric: "thermal_active", reason: metricobserver.FailureReasonInvalidReading,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			thermal, err := parseThermalReading(strings.NewReader(test.body), testThermalIdentity())
			if err != nil {
				t.Fatal(err)
			}
			if len(thermal.Issues) != 1 || thermal.Issues[0].Metric != test.metric ||
				thermal.Issues[0].Reason != test.reason {
				t.Fatalf("parseThermalReading() issues = %#v", thermal.Issues)
			}
		})
	}
}

func testCompositeIdentity(powerEndpoint, thermalEndpoint string) metricobserver.SourceIdentity {
	thermal := testThermalIdentity()
	return metricobserver.SourceIdentity{
		Kind: SourceKind, Endpoint: powerEndpoint, Metric: "wall_power_watts",
		MetricLabels:    map[string]string{"hostname": selectedNodeLabel, "device_type": "test_device"},
		ThermalEndpoint: thermalEndpoint, ThermalMetrics: &thermal,
	}
}

func testThermalIdentity() metricobserver.ThermalMetricIdentity {
	return metricobserver.ThermalMetricIdentity{
		ObservationAvailable:      "thermal_observation_available",
		Active:                    "thermal_active",
		CPUCoreThrottleEvents:     "thermal_core_events_total",
		CPUPackageThrottleEvents:  "thermal_package_events_total",
		Latched:                   "thermal_latched",
		MaximumTemperatureCelsius: "thermal_max_celsius",
	}
}

func validPowerBody() string {
	return "# TYPE wall_power_watts gauge\nwall_power_watts{hostname=\"node-7\",device_type=\"test_device\"} 5.2\n"
}
