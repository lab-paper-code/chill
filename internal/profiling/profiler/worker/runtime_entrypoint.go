package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	if err := entrypoint.Run(); err != nil {
		return fmt.Errorf("run %s entrypoint: %w", runtimeName, err)
	}
	return nil
}
