//go:build linux

package observation

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// A real kernel limit tears the failed FIFO head; explicit Flush retries it.
func TestPartialAppendIsCutBack(t *testing.T) {
	for _, stream := range []string{"definition", "management"} {
		t.Run(stream, func(t *testing.T) {
			log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			ctx := context.Background()
			if err := log.DefinitionRecord(&serving.Definition{Name: "workload", UID: "uid", Generation: 1}); err != nil {
				t.Fatal(err)
			}
			if err := log.ManagementEvent(serving.ManagementEvent{WorkloadUID: "uid", Kind: "apply", At: observationTestHour}); err != nil {
				t.Fatal(err)
			}
			if err := log.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(log.root, observationDefinitionsFile)
			if stream == "management" {
				name = filepath.Join(log.root, "20261020T10", "management.00.jsonl")
			}
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			var previous syscall.Rlimit
			if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &previous); err != nil {
				t.Fatal(err)
			}
			signal.Ignore(syscall.SIGXFSZ)
			defer signal.Reset(syscall.SIGXFSZ)
			if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: uint64(info.Size()) + 16, Max: previous.Max}); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &previous); err != nil {
					t.Error(err)
				}
			}()
			large := strings.Repeat("x", 4096)
			if stream == "definition" {
				err = log.DefinitionRecord(&serving.Definition{Name: large, UID: "uid", Generation: 2})
			} else {
				err = log.ManagementEvent(serving.ManagementEvent{WorkloadUID: "uid", Kind: "apply", Error: large, At: observationTestHour})
			}
			if err != nil {
				t.Fatalf("FIFO admission failed before storage: %v", err)
			}
			if _, err := log.writeBarrier(ctx, false); err == nil {
				t.Fatal("over-limit Write was reported complete")
			}
			if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &previous); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Count(data, []byte{'\n'}) != 1 || !json.Valid(bytes.TrimSpace(data)) {
				t.Fatalf("failed head left a torn row: %q", data)
			}
			if err := log.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
			if len(lines) != 2 {
				t.Fatalf("retry lost or duplicated the FIFO head: %d rows", len(lines))
			}
			for _, line := range lines {
				if !json.Valid(line) {
					t.Fatalf("retry appended after an unproved boundary: %q", line)
				}
			}
		})
	}
}
