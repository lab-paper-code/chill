// Package edgemetrics adapts an edge-metrics Prometheus endpoint to a CHILL
// powerobserver.Source.
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

	"github.com/lab-paper-code/chill/internal/powerobserver"
)

const (
	// ShellyPowerMetric is the edge-metrics wall-power metric observed by CHILL.
	ShellyPowerMetric    = "shelly_power_total_watts"
	maximumResponseBytes = 1 << 20
)

// Source reads one Shelly wall-power value from an edge-metrics endpoint.
type Source struct {
	identity powerobserver.SourceIdentity
	client   *http.Client
}

// New returns an edge-metrics source for an already-resolved endpoint.
func New(identity powerobserver.SourceIdentity, client *http.Client) (*Source, error) {
	if strings.TrimSpace(identity.Endpoint) == "" {
		return nil, errors.New("endpoint is required")
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	identity.Kind = "edge-metrics"
	identity.Endpoint = strings.TrimSpace(identity.Endpoint)
	identity.Metric = ShellyPowerMetric
	return &Source{identity: identity, client: client}, nil
}

// Identity returns the source and metric identity frozen for this adapter.
func (s *Source) Identity() powerobserver.SourceIdentity {
	return s.identity
}

// ReadPower returns one instantaneous wall-power reading in watts.
func (s *Source) ReadPower(ctx context.Context) (float64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.identity.Endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("build edge-metrics request: %w", err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("read edge-metrics: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return 0, fmt.Errorf("read edge-metrics: HTTP %d", response.StatusCode)
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return 0, fmt.Errorf("read edge-metrics response: %w", err)
	}
	if len(payload) > maximumResponseBytes {
		return 0, fmt.Errorf("%w: edge-metrics response exceeds %d bytes", powerobserver.ErrInvalidReading, maximumResponseBytes)
	}
	return parseMetric(bytes.NewReader(payload), ShellyPowerMetric)
}

func parseMetric(reader io.Reader, metric string) (float64, error) {
	scanner := bufio.NewScanner(reader)
	found := false
	var value float64
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
		if name != metric {
			continue
		}
		if len(fields) > 3 {
			return 0, fmt.Errorf("%w: malformed %s series", powerobserver.ErrInvalidReading, metric)
		}
		if found {
			return 0, fmt.Errorf("%w: multiple %s series", powerobserver.ErrInvalidReading, metric)
		}
		parsed, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			return 0, fmt.Errorf("%w: %s=%q", powerobserver.ErrInvalidReading, metric, fields[1])
		}
		value, found = parsed, true
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("parse edge-metrics response: %w", err)
	}
	if !found {
		return 0, fmt.Errorf("%w: %s", powerobserver.ErrMetricMissing, metric)
	}
	return value, nil
}
