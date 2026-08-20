package shellygateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/metricobserver"
)

var targetSeries = SeriesIdentity{DeviceType: "jetson_orin", Hostname: "jetson-orin"}

func TestNewValidatesAndFreezesIdentity(t *testing.T) {
	validIdentity := metricobserver.SourceIdentity{
		NodeName: "jetsono",
		Endpoint: "http://155.230.35.213:18766/prometheus",
	}
	tests := []struct {
		name     string
		identity metricobserver.SourceIdentity
		series   SeriesIdentity
		client   *http.Client
	}{
		{name: "target node missing", identity: withNode(validIdentity, ""), series: targetSeries, client: http.DefaultClient},
		{name: "target node surrounding whitespace", identity: withNode(validIdentity, " jetsono"), series: targetSeries, client: http.DefaultClient},
		{name: "device type missing", identity: validIdentity, series: SeriesIdentity{Hostname: targetSeries.Hostname}, client: http.DefaultClient},
		{name: "device type surrounding whitespace", identity: validIdentity, series: SeriesIdentity{DeviceType: "jetson_orin ", Hostname: targetSeries.Hostname}, client: http.DefaultClient},
		{name: "hostname missing", identity: validIdentity, series: SeriesIdentity{DeviceType: targetSeries.DeviceType}, client: http.DefaultClient},
		{name: "hostname surrounding whitespace", identity: validIdentity, series: SeriesIdentity{DeviceType: targetSeries.DeviceType, Hostname: " jetson-orin"}, client: http.DefaultClient},
		{name: "client missing", identity: validIdentity, series: targetSeries},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.identity, test.series, test.client); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}

	supplied := validIdentity
	supplied.Kind = "stale"
	supplied.Metric = "stale"
	supplied.ThermalMetrics = &metricobserver.ThermalMetricIdentity{Active: "stale"}
	source, err := New(supplied, SeriesIdentity{
		DeviceType: `jetson_"orin`,
		Hostname:   `jetson\orin`,
	}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	identity := source.Identity()
	if identity.Kind != "shelly-gateway" || identity.NodeName != "jetsono" ||
		identity.Endpoint != validIdentity.Endpoint ||
		identity.Metric != `shelly_power_total_watts{device_type="jetson_\"orin",hostname="jetson\\orin"}` ||
		identity.ThermalMetrics != nil {
		t.Fatalf("Identity() = %#v", identity)
	}
	identity.NodeName = "mutated"
	if source.Identity().NodeName != "jetsono" {
		t.Fatal("Identity() returned source-owned state")
	}
}

func TestNewRejectsNonCentralEndpointShape(t *testing.T) {
	tests := []string{
		"",
		" http://gateway.test:18766/prometheus",
		"https://gateway.test:18766/prometheus",
		"http://gateway.test/prometheus",
		"http://:18766/prometheus",
		"http://gateway.test:0/prometheus",
		"http://gateway.test:18766/metrics",
		"http://gateway.test:18766/%70rometheus",
		"http://user:secret@gateway.test:18766/prometheus",
		"http://gateway.test:18766/prometheus?device=x",
		"http://gateway.test:18766/prometheus?",
		"http://gateway.test:18766/prometheus#",
		"http://gateway.test:18766/prometheus#fragment",
	}
	for _, endpoint := range tests {
		t.Run(endpoint, func(t *testing.T) {
			_, err := New(metricobserver.SourceIdentity{NodeName: "jetsono", Endpoint: endpoint}, targetSeries, http.DefaultClient)
			if err == nil {
				t.Fatal("New() error = nil, want endpoint validation error")
			}
		})
	}
}

func TestParseReadingSelectsOneExactSeriesFromCentralPayload(t *testing.T) {
	payload := `
# HELP shelly_power_total_watts Current active power.
# TYPE shelly_power_total_watts gauge
shelly_power_total_watts{device_type="raspberry_pi",hostname="rasp5"} 4.1
shelly_power_total_watts{device_type="jetson_xavier",hostname="jetson-xavier"} 15.2
shelly_power_total_watts{device_type="lattepanda",hostname="lattepanda"} 7.3
shelly_power_total_watts{device_type="orangepi",hostname="orangepi"} 6.4
shelly_power_total_watts{device_type="jetson_nano",hostname="jetson-nano"} 8.5
shelly_power_total_watts{device_type="jetson_orin",hostname="jetson-orin"} 42.75
shelly_power_total_watts{device_type="genesis",hostname="genesis3"} 22.7
shelly_power_total_watts{device_type="jetson_orin",hostname="jetson-orin",serial="unexpected"} 999
shelly_power_total_watts{device_type="unmapped",hostname="unexpected",serial="unknown"} 999
unrelated_metric{device_type="jetson_orin",hostname="jetson-orin"} 1000
`
	reading, err := parseReading(strings.NewReader(payload), targetSeries)
	if err != nil {
		t.Fatal(err)
	}
	if reading.Watts != 42.75 || reading.Thermal.ObservationAvailable != nil || len(reading.Thermal.Issues) != 0 {
		t.Fatalf("parseReading() = %#v", reading)
	}
}

func TestParseReadingRejectsUnavailableOrInvalidExactSeries(t *testing.T) {
	exact := `shelly_power_total_watts{device_type="jetson_orin",hostname="jetson-orin"}`
	tests := []struct {
		name      string
		payload   string
		wantError error
	}{
		{name: "family missing", payload: "other_metric 1\n", wantError: metricobserver.ErrMetricMissing},
		{name: "exact labels missing", payload: "# TYPE shelly_power_total_watts gauge\n" +
			`shelly_power_total_watts{device_type="jetson_xavier",hostname="jetson-xavier"} 2` + "\n", wantError: metricobserver.ErrMetricMissing},
		{name: "extra label is not exact", payload: "# TYPE shelly_power_total_watts gauge\n" + exact[:len(exact)-1] + `,serial="one"} 2` + "\n", wantError: metricobserver.ErrMetricMissing},
		{name: "duplicate exact", payload: "# TYPE shelly_power_total_watts gauge\n" + exact + " 2\n" + exact + " 3\n", wantError: metricobserver.ErrInvalidReading},
		{name: "wrong family type", payload: "# TYPE shelly_power_total_watts counter\n" + exact + " 2\n", wantError: metricobserver.ErrInvalidReading},
		{name: "missing type", payload: exact + " 2\n", wantError: metricobserver.ErrInvalidReading},
		{name: "malformed", payload: "# TYPE shelly_power_total_watts gauge\n" + exact + " nope\n", wantError: metricobserver.ErrInvalidReading},
		{name: "NaN", payload: "# TYPE shelly_power_total_watts gauge\n" + exact + " NaN\n", wantError: metricobserver.ErrInvalidReading},
		{name: "positive infinity", payload: "# TYPE shelly_power_total_watts gauge\n" + exact + " +Inf\n", wantError: metricobserver.ErrInvalidReading},
		{name: "negative infinity", payload: "# TYPE shelly_power_total_watts gauge\n" + exact + " -Inf\n", wantError: metricobserver.ErrInvalidReading},
		{name: "negative", payload: "# TYPE shelly_power_total_watts gauge\n" + exact + " -0.1\n", wantError: metricobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseReading(strings.NewReader(test.payload), targetSeries)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("parseReading() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestSourceReadMetricsTransportBoundaries(t *testing.T) {
	validPayload := "# TYPE shelly_power_total_watts gauge\n" +
		`shelly_power_total_watts{device_type="jetson_orin",hostname="jetson-orin"} 9.25` + "\n"
	tests := []struct {
		name      string
		status    int
		payload   string
		want      float64
		wantError error
	}{
		{name: "success", status: http.StatusOK, payload: validPayload, want: 9.25},
		{name: "HTTP status", status: http.StatusBadGateway, payload: "failed", wantError: errors.New("HTTP")},
		{name: "oversized", status: http.StatusOK, payload: strings.Repeat("x", maximumResponseSize+1), wantError: metricobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/prometheus" {
					t.Errorf("request path = %q", request.URL.Path)
				}
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.payload))
			}))
			defer server.Close()
			source, err := New(
				metricobserver.SourceIdentity{NodeName: "jetsono", Endpoint: server.URL + "/prometheus"},
				targetSeries,
				server.Client(),
			)
			if err != nil {
				t.Fatal(err)
			}
			reading, err := source.ReadMetrics(context.Background())
			if test.wantError != nil && err == nil {
				t.Fatalf("ReadMetrics() error = nil, want %v", test.wantError)
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

func TestSourceReadMetricsReportsClientAndBodyErrors(t *testing.T) {
	clientError := errors.New("client unavailable")
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, clientError
	})}
	source, err := New(
		metricobserver.SourceIdentity{NodeName: "jetsono", Endpoint: "http://gateway.test:18766/prometheus"},
		targetSeries,
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadMetrics(context.Background()); !errors.Is(err, clientError) {
		t.Fatalf("ReadMetrics() error = %v, want client error", err)
	}

	bodyError := errors.New("body failed")
	client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: errorReader{err: bodyError}}, nil
	})
	if _, err := source.ReadMetrics(context.Background()); !errors.Is(err, bodyError) {
		t.Fatalf("ReadMetrics() error = %v, want body error", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
func (r errorReader) Close() error             { return nil }

func withNode(identity metricobserver.SourceIdentity, nodeName string) metricobserver.SourceIdentity {
	identity.NodeName = nodeName
	return identity
}

var _ io.ReadCloser = errorReader{}
