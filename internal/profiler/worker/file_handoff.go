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

	"github.com/lab-paper-code/chill/internal/powerobserver"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

type PowerResultFileConfig struct {
	Path         string
	WaitTimeout  time.Duration
	PollInterval time.Duration
	MaxBytes     int64
}

// PowerResultFile reads one atomically published PowerObserver result from the
// Job coordination volume. It never parses container logs.
type PowerResultFile struct {
	config PowerResultFileConfig
}

var _ PowerResultReader = (*PowerResultFile)(nil)

func NewPowerResultFile(config PowerResultFileConfig) (*PowerResultFile, error) {
	if !validAbsoluteFilePath(config.Path) {
		return nil, errors.New("power result path must be a clean absolute file path")
	}
	if config.WaitTimeout <= 0 || config.PollInterval <= 0 || config.PollInterval >= config.WaitTimeout {
		return nil, errors.New("power result wait timings are invalid")
	}
	if config.MaxBytes < 1 {
		return nil, errors.New("power result maximum size must be positive")
	}
	return &PowerResultFile{config: config}, nil
}

func (f *PowerResultFile) Read(ctx context.Context) (powerobserver.Result, error) {
	waitCtx, cancel := context.WithTimeout(ctx, f.config.WaitTimeout)
	defer cancel()
	ticker := time.NewTicker(f.config.PollInterval)
	defer ticker.Stop()

	for {
		var result powerobserver.Result
		err := decodeJSONFile(f.config.Path, f.config.MaxBytes, &result)
		if err == nil {
			if err := result.Validate(); err != nil {
				return powerobserver.Result{}, fmt.Errorf("validate power result file: %w", err)
			}
			return result, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return powerobserver.Result{}, fmt.Errorf("read power result file: %w", err)
		}

		select {
		case <-waitCtx.Done():
			return powerobserver.Result{}, fmt.Errorf("wait for power result file: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// LoadFrozenIntent reads one strict intent file and verifies its externally
// supplied digest before any measurement-side effect is allowed.
func LoadFrozenIntent(path, expectedDigest string, maxBytes int64) (profilingrun.Intent, error) {
	if !validAbsoluteFilePath(path) {
		return profilingrun.Intent{}, errors.New("intent path must be a clean absolute file path")
	}
	if maxBytes < 1 {
		return profilingrun.Intent{}, errors.New("intent maximum size must be positive")
	}
	var intent profilingrun.Intent
	if err := decodeJSONFile(path, maxBytes, &intent); err != nil {
		return profilingrun.Intent{}, fmt.Errorf("read frozen intent file: %w", err)
	}
	digest, err := intent.Digest()
	if err != nil {
		return profilingrun.Intent{}, fmt.Errorf("validate frozen intent file: %w", err)
	}
	if digest != expectedDigest {
		return profilingrun.Intent{}, errors.New("frozen intent file digest mismatch")
	}
	return intent, nil
}

// PublishPowerResult validates and atomically publishes one observer result
// without replacing an existing handoff.
func PublishPowerResult(path string, maxBytes int64, result powerobserver.Result) error {
	if !validAbsoluteFilePath(path) {
		return errors.New("power result path must be a clean absolute file path")
	}
	if maxBytes < 1 {
		return errors.New("power result maximum size must be positive")
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("validate power result: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode power result: %w", err)
	}
	if int64(len(payload)) > maxBytes {
		return fmt.Errorf("power result exceeds %d-byte limit", maxBytes)
	}
	if err := publishFile(path, payload); err != nil {
		return fmt.Errorf("publish power result: %w", err)
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
