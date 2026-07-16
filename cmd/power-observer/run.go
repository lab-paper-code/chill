package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/lab-paper-code/chill/internal/powerobserver"
	"github.com/lab-paper-code/chill/internal/powerobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/profiler/worker"
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
	intent, err := worker.LoadFrozenIntent(opts.intentPath, opts.intentDigest, maximumIntentBytes)
	if err != nil {
		return err
	}
	source, err := edgemetrics.New(powerobserver.SourceIdentity{
		NodeName: intent.Power.NodeName,
		Endpoint: intent.Power.Endpoint,
	}, client)
	if err != nil {
		return fmt.Errorf("construct frozen power source: %w", err)
	}
	identity := source.Identity()
	if identity.Kind != intent.Power.SourceKind || identity.NodeName != intent.Power.NodeName ||
		identity.Endpoint != intent.Power.Endpoint || identity.Metric != intent.Power.Metric {
		return errors.New("frozen power source is unsupported by the production observer")
	}
	observer, err := powerobserver.New(source)
	if err != nil {
		return err
	}
	if err := worker.PublishSignal(opts.readyPath); err != nil {
		return fmt.Errorf("publish observer-ready: %w", err)
	}
	if err := worker.WaitForSignal(ctx, opts.startPath, waitTimeout, pollInterval); err != nil {
		return fmt.Errorf("wait for measurement-start: %w", err)
	}

	result, err := observer.Observe(ctx, powerobserver.Request{
		Interval:       time.Duration(intent.Power.IntervalMilliseconds) * time.Millisecond,
		Duration:       time.Duration(intent.Power.DurationSeconds) * time.Second,
		RequestTimeout: time.Duration(intent.Power.TimeoutMilliseconds) * time.Millisecond,
		MaxEvents:      opts.maxEvents,
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("power observation interrupted: %w", err)
	}
	if err := worker.PublishPowerResult(opts.powerResultPath, maximumResultBytes, result); err != nil {
		return err
	}
	log.Printf(
		"power observation completed node=%q samples=%d failures=%d",
		result.Source.NodeName,
		result.Summary.SuccessfulSamples,
		result.Summary.Failures,
	)
	return nil
}
