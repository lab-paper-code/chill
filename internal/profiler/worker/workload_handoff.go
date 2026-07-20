package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

type WorkloadResultFileConfig struct {
	Path         string
	WaitTimeout  time.Duration
	PollInterval time.Duration
	MaxBytes     int64
}

// WorkloadResultFile reads one atomically published WorkloadObserver result
// from the Job coordination volume. It never parses container logs.
type WorkloadResultFile struct {
	config WorkloadResultFileConfig
}

func NewWorkloadResultFile(config WorkloadResultFileConfig) (*WorkloadResultFile, error) {
	if !validAbsoluteFilePath(config.Path) {
		return nil, errors.New("workload result path must be a clean absolute file path")
	}
	if config.WaitTimeout <= 0 || config.PollInterval <= 0 || config.PollInterval >= config.WaitTimeout {
		return nil, errors.New("workload result wait timings are invalid")
	}
	if config.MaxBytes < 1 {
		return nil, errors.New("workload result maximum size must be positive")
	}
	return &WorkloadResultFile{config: config}, nil
}

func (f *WorkloadResultFile) Read(ctx context.Context) (workloadobserver.Result, error) {
	waitCtx, cancel := context.WithTimeout(ctx, f.config.WaitTimeout)
	defer cancel()
	ticker := time.NewTicker(f.config.PollInterval)
	defer ticker.Stop()

	for {
		var result workloadobserver.Result
		err := decodeJSONFile(f.config.Path, f.config.MaxBytes, &result)
		if err == nil {
			if err := result.Validate(); err != nil {
				return workloadobserver.Result{}, fmt.Errorf("validate workload result file: %w", err)
			}
			return result, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return workloadobserver.Result{}, fmt.Errorf("read workload result file: %w", err)
		}

		select {
		case <-waitCtx.Done():
			return workloadobserver.Result{}, fmt.Errorf("wait for workload result file: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// PublishWorkloadResult validates and atomically publishes one observer result
// without replacing an existing handoff.
func PublishWorkloadResult(path string, maxBytes int64, result workloadobserver.Result) error {
	if !validAbsoluteFilePath(path) {
		return errors.New("workload result path must be a clean absolute file path")
	}
	if maxBytes < 1 {
		return errors.New("workload result maximum size must be positive")
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate workload result: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode workload result: %w", err)
	}
	if int64(len(payload)) > maxBytes {
		return fmt.Errorf("workload result exceeds %d-byte limit", maxBytes)
	}
	if err := publishFile(path, payload); err != nil {
		return fmt.Errorf("publish workload result: %w", err)
	}
	return nil
}
