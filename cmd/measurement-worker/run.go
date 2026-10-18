package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/worker"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

type measurementCoordinator interface {
	Execute(context.Context, worker.Request) (evidencestore.Receipt, error)
}

type coordinatorBuilder func(
	options,
	profilingrun.VerifiedRunConfig,
	io.Writer,
	io.Writer,
) (measurementCoordinator, error)

type dependencies struct {
	parseRunConfig   func(string) (profilingrun.VerifiedRunConfig, error)
	buildCoordinator coordinatorBuilder
	writeReceipt     func(string, evidencestore.Receipt) error
}

func productionDependencies() dependencies {
	return dependencies{
		parseRunConfig:   worker.ParseVerifiedRunConfig,
		buildCoordinator: buildCoordinator,
		writeReceipt:     writeReceipt,
	}
}

func execute(
	ctx context.Context,
	opts options,
	stdout, stderr io.Writer,
	deps dependencies,
) error {
	runConfig, err := deps.parseRunConfig(opts.runConfig)
	if err != nil {
		return fmt.Errorf("%w: load RunConfig: %w", worker.ErrExecutionContract, err)
	}
	coordinator, err := deps.buildCoordinator(opts, runConfig, stdout, stderr)
	if err != nil {
		return fmt.Errorf("%w: construct worker: %w", worker.ErrExecutionContract, err)
	}
	receipt, err := coordinator.Execute(ctx, worker.Request{
		RunConfig:         runConfig,
		PodUID:            opts.podUID,
		ScheduledNodeName: opts.scheduledNodeName,
		RawLocation: evidencestore.ObjectLocation{
			Store: opts.storeName,
			Key:   opts.rawObjectKey,
		},
	})
	if err != nil {
		return err
	}
	if err := deps.writeReceipt(opts.terminationMessagePath, receipt); err != nil {
		return fmt.Errorf("%w: write success receipt: %w", worker.ErrEvidenceCommit, err)
	}
	return nil
}

func buildCoordinator(
	opts options,
	runConfig profilingrun.VerifiedRunConfig,
	stdout, stderr io.Writer,
) (measurementCoordinator, error) {
	runtimeAdapter, err := buildRuntimeAdapter(opts, runConfig, stdout, stderr)
	if err != nil {
		return nil, err
	}
	metricResult, err := worker.NewMetricResultFile(worker.MetricResultFileConfig{
		Path: opts.metricResultPath, WaitTimeout: opts.metricResultTimeout,
		PollInterval: handoffPollInterval, MaxBytes: opts.maxMetricResultBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("construct metric-result reader: %w", err)
	}
	workloadResult, err := worker.NewWorkloadResultFile(worker.WorkloadResultFileConfig{
		Path: opts.workloadResultPath, WaitTimeout: opts.workloadResultTimeout,
		PollInterval: handoffPollInterval, MaxBytes: opts.maxWorkloadResultBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("construct workload-result reader: %w", err)
	}
	store, err := evidences3.New(evidences3.Config{
		LogicalStore: opts.storeName, Bucket: opts.storeBucket,
		Endpoint: opts.storeEndpoint, Region: opts.storeRegion,
		AccessKeyID: opts.accessKeyID, SecretAccessKey: opts.secretAccessKey,
		SessionToken: opts.sessionToken, TempDir: opts.workspacePath,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Evidence Store writer: %w", err)
	}
	coordinator, err := worker.NewCoordinator(runtimeAdapter, metricResult, workloadResult, store)
	if err != nil {
		return nil, fmt.Errorf("construct measurement coordinator: %w", err)
	}
	return coordinator, nil
}

func buildRuntimeAdapter(
	opts options,
	runConfig profilingrun.VerifiedRunConfig,
	stdout, stderr io.Writer,
) (worker.Runtime, error) {
	runtimeAdapter, err := worker.NewFirstfloorRuntime(worker.CPURuntimeConfig{
		Command:            opts.firstfloorRuntimeCommand,
		ObserverReadyPaths: opts.observerReadyPaths, WorkloadWindowStartPath: opts.workloadWindowStartPath,
		MeasurementStartPath:    opts.measurementStartPath,
		WorkloadWindowReadyPath: opts.workloadWindowReadyPath,
		MeasurementEndPath:      opts.measurementEndPath,
		ResultPath:              opts.runtimeResultPath(), ObserverReadyTimeout: opts.observerReadyTimeout,
		MaxLatencySamples: opts.maxLatencySamples, MaxInputBytes: opts.maxInputBytes,
		MaxResultBytes: opts.maxRuntimeResultBytes, Stdout: stdout, Stderr: stderr,
	}, runConfig)
	if err != nil {
		return nil, fmt.Errorf("construct firstfloor adapter: %w", err)
	}
	return runtimeAdapter, nil
}

func writeReceipt(path string, receipt evidencestore.Receipt) error {
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("validate receipt: %w", err)
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode receipt: %w", err)
	}
	if len(payload) > maximumReceiptBytes {
		return errors.New("receipt exceeds Kubernetes termination-message limit")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open termination message: %w", err)
	}
	written, err := file.Write(payload)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("write termination message: %w", err)
	}
	if written != len(payload) {
		_ = file.Close()
		return fmt.Errorf("write termination message: %w", io.ErrShortWrite)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close termination message: %w", err)
	}
	return nil
}
