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

func TestNewPowerSeriesValidatesAndOwnsExactLabels(t *testing.T) {
	valid := testPowerSeriesIdentity("http://example.test/custom")
	wrongKind := testPowerSeriesIdentity(valid.Endpoint)
	wrongKind.Kind = "other"
	missingMetric := testPowerSeriesIdentity(valid.Endpoint)
	missingMetric.Metric = ""
	missingLabels := testPowerSeriesIdentity(valid.Endpoint)
	missingLabels.MetricLabels = nil
	emptyLabels := testPowerSeriesIdentity(valid.Endpoint)
	emptyLabels.MetricLabels = map[string]string{}
	invalidLabelName := testPowerSeriesIdentity(valid.Endpoint)
	invalidLabelName.MetricLabels = map[string]string{"not-valid": "value"}
	invalidLabelValue := testPowerSeriesIdentity(valid.Endpoint)
	invalidLabelValue.MetricLabels = map[string]string{"unit": string([]byte{0xff})}
	withThermal := testPowerSeriesIdentity(valid.Endpoint)
	withThermal.ThermalMetrics = testSourceIdentity("").ThermalMetrics

	tests := []struct {
		name     string
		identity metricobserver.SourceIdentity
		client   *http.Client
		want     string
	}{
		{name: "unsupported kind", identity: wrongKind, client: http.DefaultClient, want: "unsupported edge-metrics source kind"},
		{name: "missing endpoint", identity: testPowerSeriesIdentity(""), client: http.DefaultClient, want: "endpoint is required"},
		{name: "endpoint whitespace", identity: testPowerSeriesIdentity(" http://example.test/custom "), client: http.DefaultClient, want: "without surrounding whitespace"},
		{name: "missing metric", identity: missingMetric, client: http.DefaultClient, want: "power metric identity is required"},
		{name: "missing labels", identity: missingLabels, client: http.DefaultClient, want: "exact power label selector is required"},
		{name: "empty labels", identity: emptyLabels, client: http.DefaultClient, want: "exact power label selector is required"},
		{name: "invalid label name", identity: invalidLabelName, client: http.DefaultClient, want: "label name"},
		{name: "invalid label value", identity: invalidLabelValue, client: http.DefaultClient, want: "invalid UTF-8"},
		{name: "thermal identity", identity: withThermal, client: http.DefaultClient, want: "thermal metric identity is not supported"},
		{name: "missing client", identity: valid, want: "HTTP client is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewPowerSeries(test.identity, test.client); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewPowerSeries() error = %v, want containing %q", err, test.want)
			}
		})
	}

	identity := testPowerSeriesIdentity(valid.Endpoint)
	expectedUnit := identity.MetricLabels["unit"]
	source, err := NewPowerSeries(identity, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	identity.MetricLabels["unit"] = "caller-mutation"
	got := source.Identity()
	if got.MetricLabels["unit"] != expectedUnit {
		t.Fatalf("owned identity labels = %#v", got.MetricLabels)
	}
	got.MetricLabels["unit"] = "returned-mutation"
	if source.Identity().MetricLabels["unit"] != expectedUnit {
		t.Fatal("source identity retained a caller-owned exact label map")
	}
}

func TestPowerSeriesSourceSelectsOneOfSevenConfiguredSeries(t *testing.T) {
	identity := testPowerSeriesIdentity("")
	payload := strings.Join([]string{
		"# HELP experiment_input_watts Arbitrary test power.",
		"# TYPE experiment_input_watts gauge",
		`experiment_input_watts{feed="a",unit="node-1"} 1.1`,
		`experiment_input_watts{feed="a",unit="node-2"} 2.2`,
		`experiment_input_watts{feed="b",unit="node-3"} 3.3`,
		`experiment_input_watts{feed="b",unit="node-4"} 4.4`,
		`experiment_input_watts{feed="c",unit="node-5"} 5.5`,
		`experiment_input_watts{feed="c",unit="node-6"} 6.6`,
		`experiment_input_watts{unit="node-7",feed="b"} 7.7`,
	}, "\n") + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		hostnames := request.URL.Query()["hostname"]
		if len(request.URL.Query()) != 1 || len(hostnames) != 1 || hostnames[0] != "node-7" {
			t.Errorf("request query = %q", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte(payload))
	}))
	defer server.Close()
	identity.Endpoint = server.URL + "?hostname=node-7"

	source, err := NewPowerSeries(identity, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	reading, err := source.ReadMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reading.Watts != 7.7 || reading.Thermal.ObservationAvailable != nil || reading.Thermal.Active != nil ||
		reading.Thermal.CPUCoreThrottleEvents != nil || reading.Thermal.CPUPackageThrottleEvents != nil ||
		reading.Thermal.Latched != nil || reading.Thermal.MaximumTemperatureCelsius != nil ||
		len(reading.Thermal.Issues) != 0 {
		t.Fatalf("reading = %#v", reading)
	}
}

func TestParsePowerSeriesFailsClosed(t *testing.T) {
	identity := testPowerSeriesIdentity("")
	metric := identity.Metric
	exact := `{feed="b",unit="node-7"}`
	tests := []struct {
		name      string
		payload   string
		wantError error
	}{
		{name: "metric missing", payload: "# TYPE other gauge\nother 1\n", wantError: metricobserver.ErrMetricMissing},
		{name: "labels missing", payload: "# TYPE " + metric + " gauge\n" + metric + `{feed="b"} 1` + "\n", wantError: metricobserver.ErrMetricMissing},
		{name: "extra label", payload: "# TYPE " + metric + " gauge\n" + metric + `{feed="b",unit="node-7",zone="x"} 1` + "\n", wantError: metricobserver.ErrMetricMissing},
		{name: "wrong value", payload: "# TYPE " + metric + " gauge\n" + metric + `{feed="b",unit="other"} 1` + "\n", wantError: metricobserver.ErrMetricMissing},
		{name: "multiple", payload: "# TYPE " + metric + " gauge\n" + metric + exact + " 1\n" + metric + exact + " 2\n", wantError: metricobserver.ErrInvalidReading},
		{name: "counter type", payload: "# TYPE " + metric + " counter\n" + metric + exact + " 1\n", wantError: metricobserver.ErrInvalidReading},
		{name: "malformed", payload: "# TYPE " + metric + " gauge\n" + metric + `{feed="b",unit="node-7" 1` + "\n", wantError: metricobserver.ErrInvalidReading},
		{name: "duplicate label name", payload: "# TYPE " + metric + " gauge\n" + metric + `{feed="b",feed="b",unit="node-7"} 1` + "\n", wantError: metricobserver.ErrInvalidReading},
		{name: "NaN", payload: "# TYPE " + metric + " gauge\n" + metric + exact + " NaN\n", wantError: metricobserver.ErrInvalidReading},
		{name: "positive infinity", payload: "# TYPE " + metric + " gauge\n" + metric + exact + " +Inf\n", wantError: metricobserver.ErrInvalidReading},
		{name: "negative infinity", payload: "# TYPE " + metric + " gauge\n" + metric + exact + " -Inf\n", wantError: metricobserver.ErrInvalidReading},
		{name: "negative", payload: "# TYPE " + metric + " gauge\n" + metric + exact + " -0.1\n", wantError: metricobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parsePowerSeries(strings.NewReader(test.payload), metric, identity.MetricLabels)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("parsePowerSeries() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestPowerSeriesSourceReusesBoundedTransport(t *testing.T) {
	identity := testPowerSeriesIdentity("")
	tests := []struct {
		name      string
		status    int
		payload   string
		wantError error
	}{
		{name: "HTTP failure", status: http.StatusBadGateway, payload: "failed", wantError: errors.New("HTTP failure")},
		{name: "oversized response", status: http.StatusOK, payload: strings.Repeat("x", maximumResponseBytes+1), wantError: metricobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.payload))
			}))
			defer server.Close()
			candidate := identity
			candidate.Endpoint = server.URL
			source, err := NewPowerSeries(candidate, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = source.ReadMetrics(context.Background())
			if err == nil {
				t.Fatalf("ReadMetrics() error = nil, want %v", test.wantError)
			}
			if errors.Is(test.wantError, metricobserver.ErrInvalidReading) && !errors.Is(err, metricobserver.ErrInvalidReading) {
				t.Fatalf("ReadMetrics() error = %v, want invalid reading", err)
			}
		})
	}
}

func TestPowerSeriesSourceHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	identity := testPowerSeriesIdentity(server.URL)
	source, err := NewPowerSeries(identity, server.Client())
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

func testPowerSeriesIdentity(endpoint string) metricobserver.SourceIdentity {
	return metricobserver.SourceIdentity{
		Kind: SourceKind, Endpoint: endpoint, Metric: "experiment_input_watts",
		MetricLabels: map[string]string{"feed": "b", "unit": "node-7"},
	}
}
