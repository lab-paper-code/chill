package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

type MetricResultFileConfig struct {
	Path         string
	WaitTimeout  time.Duration
	PollInterval time.Duration
	MaxBytes     int64
}

// MetricResultFile reads one atomically published MetricObserver result from the
// Job coordination volume. It never parses container logs.
type MetricResultFile struct {
	config MetricResultFileConfig
}

var _ MetricResultReader = (*MetricResultFile)(nil)

func NewMetricResultFile(config MetricResultFileConfig) (*MetricResultFile, error) {
	if !validAbsoluteFilePath(config.Path) {
		return nil, errors.New("metric result path must be a clean absolute file path")
	}
	if config.WaitTimeout <= 0 || config.PollInterval <= 0 || config.PollInterval >= config.WaitTimeout {
		return nil, errors.New("metric result wait timings are invalid")
	}
	if config.MaxBytes < 1 {
		return nil, errors.New("metric result maximum size must be positive")
	}
	return &MetricResultFile{config: config}, nil
}

func (f *MetricResultFile) Read(ctx context.Context) (metricobserver.VerifiedResult, error) {
	waitCtx, cancel := context.WithTimeout(ctx, f.config.WaitTimeout)
	defer cancel()
	ticker := time.NewTicker(f.config.PollInterval)
	defer ticker.Stop()

	for {
		var result metricobserver.Result
		err := decodeJSONFile(f.config.Path, f.config.MaxBytes, &result)
		if err == nil {
			verified, err := metricobserver.VerifyResult(result)
			if err != nil {
				return metricobserver.VerifiedResult{}, fmt.Errorf("validate metric result file: %w", err)
			}
			return verified, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return metricobserver.VerifiedResult{}, fmt.Errorf("read metric result file: %w", err)
		}

		select {
		case <-waitCtx.Done():
			return metricobserver.VerifiedResult{}, fmt.Errorf("wait for metric result file: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// LoadVerifiedIntent reads and validates one strict frozen-intent file before
// any measurement-side effect is allowed.
func LoadVerifiedIntent(path string, maxBytes int64) (profilingrun.VerifiedIntent, error) {
	if !validAbsoluteFilePath(path) {
		return profilingrun.VerifiedIntent{}, errors.New("intent path must be a clean absolute file path")
	}
	if maxBytes < 1 {
		return profilingrun.VerifiedIntent{}, errors.New("intent maximum size must be positive")
	}
	var intent profilingrun.Intent
	if err := decodeProjectedJSONFile(path, maxBytes, &intent); err != nil {
		return profilingrun.VerifiedIntent{}, fmt.Errorf("read frozen intent file: %w", err)
	}
	verified, err := profilingrun.ValidateIntent(intent)
	if err != nil {
		return profilingrun.VerifiedIntent{}, fmt.Errorf("validate frozen intent file: %w", err)
	}
	return verified, nil
}

// LoadVerifiedCandidateReport reads the exact projected candidate report and
// verifies it once against the already-frozen intent. Only the sealed result
// crosses into runtime construction.
func LoadVerifiedCandidateReport(
	path string,
	maxBytes int64,
	intent profilingrun.VerifiedIntent,
) (profilingrun.VerifiedCandidateReport, error) {
	if !validAbsoluteFilePath(path) {
		return profilingrun.VerifiedCandidateReport{}, errors.New("candidate report path must be a clean absolute file path")
	}
	if maxBytes < 1 {
		return profilingrun.VerifiedCandidateReport{}, errors.New("candidate report maximum size must be positive")
	}
	intentValue, err := intent.Record()
	if err != nil {
		return profilingrun.VerifiedCandidateReport{}, err
	}
	payload, err := readProjectedFile(path, maxBytes)
	if err != nil {
		return profilingrun.VerifiedCandidateReport{}, fmt.Errorf("read candidate report file: %w", err)
	}
	verified, err := profilingrun.VerifyCandidateReport(payload, intentValue)
	if err != nil {
		return profilingrun.VerifiedCandidateReport{}, fmt.Errorf("verify candidate report file: %w", err)
	}
	return verified, nil
}

// PublishMetricResult validates and atomically publishes one observer result
// without replacing an existing handoff.
func PublishMetricResult(path string, maxBytes int64, result metricobserver.Result) error {
	if !validAbsoluteFilePath(path) {
		return errors.New("metric result path must be a clean absolute file path")
	}
	if maxBytes < 1 {
		return errors.New("metric result maximum size must be positive")
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate metric result: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode metric result: %w", err)
	}
	if int64(len(payload)) > maxBytes {
		return fmt.Errorf("metric result exceeds %d-byte limit", maxBytes)
	}
	if err := publishFile(path, payload); err != nil {
		return fmt.Errorf("publish metric result: %w", err)
	}
	return nil
}

// PublishSignal atomically creates one presence-only coordination signal and
// refuses to replace a pre-existing path.
func PublishSignal(path string) error {
	if !validAbsoluteFilePath(path) {
		return errors.New("signal path must be a clean absolute file path")
	}
	if err := publishFile(path, []byte("signal\n")); err != nil {
		return fmt.Errorf("publish signal: %w", err)
	}
	return nil
}

// WaitForSignal waits for a regular signal file within a bounded interval.
func WaitForSignal(ctx context.Context, path string, timeout, pollInterval time.Duration) error {
	if !validAbsoluteFilePath(path) {
		return errors.New("signal path must be a clean absolute file path")
	}
	if timeout <= 0 || pollInterval <= 0 || pollInterval >= timeout {
		return errors.New("signal wait timings are invalid")
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("signal path is not a regular file")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect signal path: %w", err)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for signal: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func publishFile(path string, payload []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("handoff path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".chill-handoff-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func decodeJSONFile(path string, maxBytes int64, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("handoff path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return errors.New("handoff path changed while opening")
	}
	return decodeOpenedJSONFile(file, maxBytes, target)
}

// decodeProjectedJSONFile follows a Kubernetes projected-volume symlink, then
// verifies and decodes the regular file held by the opened descriptor.
func decodeProjectedJSONFile(path string, maxBytes int64, target any) error {
	payload, err := readProjectedFile(path, maxBytes)
	if err != nil {
		return err
	}
	return decodeJSON(payload, target)
}

func readProjectedFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("projected input target is not a regular file")
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("handoff file exceeds %d-byte limit", maxBytes)
	}
	return payload, nil
}

func decodeOpenedJSONFile(file *os.File, maxBytes int64, target any) error {
	payload, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if int64(len(payload)) > maxBytes {
		return fmt.Errorf("handoff file exceeds %d-byte limit", maxBytes)
	}

	return decodeJSON(payload, target)
}

func decodeJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("handoff file contains a trailing JSON value")
	}
	return nil
}

func validAbsoluteFilePath(value string) bool {
	return filepath.IsAbs(value) && value != string(filepath.Separator) && filepath.Clean(value) == value
}
