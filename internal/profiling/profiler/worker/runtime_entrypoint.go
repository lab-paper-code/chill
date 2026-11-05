package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
)

func runRuntimeEntrypoint(
	ctx context.Context,
	runtimeName string,
	command []string,
	resultPath string,
	payload []byte,
	stdout, stderr io.Writer,
) error {
	if _, err := os.Lstat(resultPath); err == nil {
		return errors.New(runtimeName + " result path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s result path: %w", runtimeName, err)
	}
	arguments := append([]string(nil), command[1:]...)
	arguments = append(arguments, "--result-file", resultPath)
	entrypoint := exec.CommandContext(ctx, command[0], arguments...)
	entrypoint.Stdin = bytes.NewReader(payload)
	entrypoint.Stdout = stdout
	entrypoint.Stderr = stderr
	if path := terminationMessagePath(ctx); path != "" {
		entrypoint.Env = append(os.Environ(), terminationPathEnvironment+"="+path)
	}
	if err := entrypoint.Run(); err != nil {
		if message, readErr := os.ReadFile(terminationMessagePath(ctx)); readErr == nil {
			if record, ok := evidence.DecodeTerminationMessage(string(message)); ok && record.FailureReason == evidence.FailureIntegrity {
				return fmt.Errorf("%w: run %s entrypoint: %w", ErrExecutionContract, runtimeName, err)
			}
		}
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		return fmt.Errorf("run %s entrypoint: %w", runtimeName, err)
	}
	return nil
}
