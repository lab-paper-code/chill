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
