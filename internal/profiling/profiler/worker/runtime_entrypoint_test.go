package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const runtimeEntrypointHelperEnv = "CHILL_RUNTIME_ENTRYPOINT_HELPER"

func TestRunRuntimeEntrypointPreservesProcessContract(t *testing.T) {
	t.Setenv(runtimeEntrypointHelperEnv, "1")
	resultPath := filepath.Join(t.TempDir(), "result.json")
	payload := []byte(`{"request":"payload"}`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := runRuntimeEntrypoint(
		context.Background(),
		"test runtime",
		[]string{os.Args[0], "-test.run=TestRuntimeEntrypointHelperProcess", "--", "--fixed-argument"},
		resultPath,
		payload,
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, payload) || stdout.String() != "entrypoint stdout" || stderr.String() != "entrypoint stderr" {
		t.Fatalf("entrypoint contract: result=%q stdout=%q stderr=%q", result, stdout.String(), stderr.String())
	}
}

func TestRunRuntimeEntrypointRejectsUnusableResultPath(t *testing.T) {
	for _, runtimeName := range []string{"CPU ORT", "RKNNLite"} {
		t.Run(runtimeName+" stale", func(t *testing.T) {
			resultPath := filepath.Join(t.TempDir(), "result.json")
			if err := os.WriteFile(resultPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			err := runRuntimeEntrypoint(
				context.Background(), runtimeName, []string{"must-not-run"}, resultPath, nil, io.Discard, io.Discard,
			)
			if err == nil || err.Error() != runtimeName+" result path already exists" {
				t.Fatalf("stale result error = %v", err)
			}
		})

		t.Run(runtimeName+" inspection", func(t *testing.T) {
			resultPath := filepath.Join(t.TempDir(), strings.Repeat("x", 300))
			err := runRuntimeEntrypoint(
				context.Background(), runtimeName, []string{"must-not-run"}, resultPath, nil, io.Discard, io.Discard,
			)
			if err == nil || !strings.Contains(err.Error(), "inspect "+runtimeName+" result path:") {
				t.Fatalf("result inspection error = %v", err)
			}
		})
	}
}

func TestRuntimeAdaptersPreserveEntrypointCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		measure func(*testing.T, context.Context) error
	}{
		{
			name: "CPU ORT",
			measure: func(t *testing.T, ctx context.Context) error {
				runtime := newTestORTCPURuntime(t, filepath.Join(t.TempDir(), "result.json"))
				_, err := runtime.Measure(ctx, runtimeRequest(testIntent(t)))
				return err
			},
		},
		{
			name: "RKNNLite",
			measure: func(t *testing.T, ctx context.Context) error {
				runtime := newTestRKNNLiteRuntime(t, filepath.Join(t.TempDir(), "result.json"), testRKNNDelivery())
				_, err := runtime.Measure(ctx, runtimeRequest(testRKNNIntent(t, false)))
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := test.measure(t, ctx)
			if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "run "+test.name+" entrypoint:") {
				t.Fatalf("canceled %s entrypoint error = %v", test.name, err)
			}
		})
	}
}

func TestRuntimeEntrypointHelperProcess(t *testing.T) {
	if os.Getenv(runtimeEntrypointHelperEnv) == "" {
		return
	}
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(30)
	}
	resultPath := ""
	fixedArgument := false
	for index, argument := range os.Args {
		if argument == "--fixed-argument" {
			fixedArgument = true
		}
		if argument == "--result-file" && index+1 < len(os.Args) {
			resultPath = os.Args[index+1]
		}
	}
	if !fixedArgument || resultPath == "" {
		os.Exit(31)
	}
	if _, err := fmt.Fprint(os.Stdout, "entrypoint stdout"); err != nil {
		os.Exit(32)
	}
	if _, err := fmt.Fprint(os.Stderr, "entrypoint stderr"); err != nil {
		os.Exit(33)
	}
	if err := os.WriteFile(resultPath, payload, 0o600); err != nil {
		os.Exit(34)
	}
	os.Exit(0)
}
