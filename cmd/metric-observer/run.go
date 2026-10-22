package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
)

const (
	signalPollInterval = 10 * time.Millisecond
)

func execute(
	ctx context.Context,
	opts options,
	client *http.Client,
	waitTimeout,
	pollInterval time.Duration,
) error {
	if client == nil {
		return errors.New("HTTP client is required")
	}
	ctx, stop := worker.StopOnRuntimeFailure(ctx, opts.runtimeFailedPath, pollInterval)
	defer stop()
	runConfig, err := worker.ParseVerifiedRunConfig(opts.runConfig)
	if err != nil {
		return err
	}
	runConfigRecord, err := runConfig.Record()
	if err != nil {
		return err
	}
	metrics := runConfigRecord.Metrics
	thermalMetrics := metricobserver.ThermalMetricIdentity{
		ObservationAvailable:      metrics.ThermalMetrics.ObservationAvailable,
		Active:                    metrics.ThermalMetrics.Active,
		CPUCoreThrottleEvents:     metrics.ThermalMetrics.CPUCoreThrottleEvents,
		CPUPackageThrottleEvents:  metrics.ThermalMetrics.CPUPackageThrottleEvents,
		Latched:                   metrics.ThermalMetrics.Latched,
		MaximumTemperatureCelsius: metrics.ThermalMetrics.MaximumTemperatureCelsius,
	}
	source, err := edgemetrics.New(metricobserver.SourceIdentity{
		Kind:            metrics.SourceKind,
		NodeName:        metrics.NodeName,
		Endpoint:        metrics.Endpoint,
		Metric:          metrics.PowerMetric,
		MetricLabels:    metrics.MetricLabels,
		ThermalEndpoint: metrics.ThermalEndpoint,
		ThermalMetrics:  &thermalMetrics,
	}, client)
	if err != nil {
		return fmt.Errorf("construct metric source from RunConfig: %w", err)
	}
	observer, err := metricobserver.New(source)
	if err != nil {
		return err
	}
	if err := worker.PublishSignal(opts.readyPath); err != nil {
		return fmt.Errorf("publish observer-ready: %w", err)
	}
	if err := worker.WaitForSignal(ctx, opts.startPath, waitTimeout, pollInterval); err != nil {
		return fmt.Errorf("wait for measurement-start: %w", err)
	}

	result, err := observer.Observe(ctx, metricobserver.Request{
		Interval:       time.Duration(metrics.IntervalMilliseconds) * time.Millisecond,
		Duration:       time.Duration(metrics.DurationSeconds) * time.Second,
		RequestTimeout: time.Duration(metrics.TimeoutMilliseconds) * time.Millisecond,
		MaxEvents:      opts.maxEvents,
	}, func() {})
	if cause := context.Cause(ctx); cause != nil {
		return fmt.Errorf("metric observation interrupted: %w", cause)
	}
	if err != nil {
		return err
	}
	if err := worker.WaitForSignal(ctx, opts.endPath, opts.endWaitTimeout, pollInterval); err != nil {
		return fmt.Errorf("wait for measurement-end: %w", err)
	}
	if err := worker.PublishMetricResult(opts.metricResultPath, maximumResultBytes, result); err != nil {
		return err
	}
	thermal := summarizeThermal(result)
	log.Printf(
		"metric observation completed\n"+
			"  node: %s\n"+
			"  power samples: %d successful, %d failed reads\n"+
			"  thermal samples: %d typed, %d observable, %d active\n"+
			"  thermal metric issues: %d\n"+
			"  maximum temperature: %s",
		result.Source.NodeName,
		result.Summary.SuccessfulSamples,
		result.Summary.Failures,
		thermal.typed,
		thermal.available,
		thermal.active,
		thermal.issues,
		thermal.maximumTemperature,
	)
	return nil
}

type thermalLogSummary struct {
	typed              int
	available          int
	active             int
	issues             int
	maximumTemperature string
}

func summarizeThermal(result metricobserver.Result) thermalLogSummary {
	summary := thermalLogSummary{maximumTemperature: "unavailable"}
	var maximum *float64
	for _, sample := range result.Samples {
		reading := sample.Thermal
		if reading == nil {
			continue
		}
		summary.typed++
		if reading.ObservationAvailable != nil && *reading.ObservationAvailable {
			summary.available++
		}
		if reading.Active != nil && *reading.Active {
			summary.active++
		}
		summary.issues += len(reading.Issues)
		if reading.MaximumTemperatureCelsius != nil &&
			(maximum == nil || *reading.MaximumTemperatureCelsius > *maximum) {
			value := *reading.MaximumTemperatureCelsius
			maximum = &value
		}
	}
	if maximum != nil {
		summary.maximumTemperature = fmt.Sprintf("%.3f C", *maximum)
	}
	return summary
}
