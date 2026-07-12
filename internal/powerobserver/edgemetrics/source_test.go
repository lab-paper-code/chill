package edgemetrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/powerobserver"
)

func TestNewValidatesDependenciesAndFreezesIdentity(t *testing.T) {
	if _, err := New(powerobserver.SourceIdentity{}, http.DefaultClient); err == nil {
		t.Fatal("New() error = nil, want endpoint validation error")
	}
	if _, err := New(powerobserver.SourceIdentity{Endpoint: "http://example.test/metrics"}, nil); err == nil {
		t.Fatal("New() error = nil, want client validation error")
	}
	source, err := New(powerobserver.SourceIdentity{
		NodeName: "jetsonx", Namespace: "monitoring", PodName: "exporter", Endpoint: " http://example.test/metrics ",
	}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	identity := source.Identity()
	if identity.Kind != "edge-metrics" || identity.Metric != ShellyPowerMetric || identity.Endpoint != "http://example.test/metrics" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestParseMetric(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		want      float64
		wantError error
	}{
		{name: "plain", payload: "shelly_power_total_watts 6.4\n", want: 6.4},
		{name: "labels and timestamp", payload: "shelly_power_total_watts{node=\"lattepanda\"} 6.5 1234\n", want: 6.5},
		{name: "missing", payload: "other_metric 1\n", wantError: powerobserver.ErrMetricMissing},
		{name: "duplicate", payload: "shelly_power_total_watts 1\nshelly_power_total_watts 2\n", wantError: powerobserver.ErrInvalidReading},
		{name: "not a number", payload: "shelly_power_total_watts nope\n", wantError: powerobserver.ErrInvalidReading},
		{name: "NaN", payload: "shelly_power_total_watts NaN\n", wantError: powerobserver.ErrInvalidReading},
		{name: "positive infinity", payload: "shelly_power_total_watts +Inf\n", wantError: powerobserver.ErrInvalidReading},
		{name: "negative", payload: "shelly_power_total_watts -1\n", wantError: powerobserver.ErrInvalidReading},
		{name: "extra fields", payload: "shelly_power_total_watts 1 2 extra\n", wantError: powerobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := parseMetric(strings.NewReader(test.payload), ShellyPowerMetric)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("parseMetric() error = %v, want %v", err, test.wantError)
			}
			if err == nil && value != test.want {
				t.Fatalf("parseMetric() value = %v, want %v", value, test.want)
			}
		})
	}
}

func TestSourceReadPower(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		payload   string
		want      float64
		wantError error
	}{
		{name: "success", status: http.StatusOK, payload: "shelly_power_total_watts 4.2\n", want: 4.2},
		{name: "HTTP failure", status: http.StatusInternalServerError, payload: "failed", wantError: errors.New("HTTP failure")},
		{name: "missing metric", status: http.StatusOK, payload: "other_metric 1\n", wantError: powerobserver.ErrMetricMissing},
		{name: "oversized response", status: http.StatusOK, payload: strings.Repeat("x", maximumResponseBytes+1), wantError: powerobserver.ErrInvalidReading},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.payload))
			}))
			defer server.Close()
			source, err := New(powerobserver.SourceIdentity{Endpoint: server.URL}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			value, err := source.ReadPower(context.Background())
			if test.wantError != nil && err == nil {
				t.Fatalf("ReadPower() error = nil, want %v", test.wantError)
			}
			if errors.Is(test.wantError, powerobserver.ErrMetricMissing) && !errors.Is(err, powerobserver.ErrMetricMissing) {
				t.Fatalf("ReadPower() error = %v, want metric missing", err)
			}
			if errors.Is(test.wantError, powerobserver.ErrInvalidReading) && !errors.Is(err, powerobserver.ErrInvalidReading) {
				t.Fatalf("ReadPower() error = %v, want invalid reading", err)
			}
			if test.wantError == nil && (err != nil || value != test.want) {
				t.Fatalf("ReadPower() = %v, %v; want %v, nil", value, err, test.want)
			}
		})
	}
}

func TestSourceReadPowerHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	source, err := New(powerobserver.SourceIdentity{Endpoint: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = source.ReadPower(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReadPower() error = %v, want deadline exceeded", err)
	}
}

func TestSourceReadPowerRejectsMalformedEndpoint(t *testing.T) {
	source, err := New(powerobserver.SourceIdentity{Endpoint: "://bad"}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ReadPower(context.Background())
	if err == nil || !strings.Contains(err.Error(), "build edge-metrics request") {
		t.Fatalf("ReadPower() error = %v, want request construction error", err)
	}
}
