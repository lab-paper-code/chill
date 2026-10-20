//go:build linux

package serving

import (
	"bytes"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A file size limit makes the kernel write a prefix and fail, as ENOSPC does.
func TestPartialAppendIsCutBack(t *testing.T) {
	observationLog, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	definition := &Definition{Name: "workload", UID: "uid", Generation: 1}
	if err := observationLog.DefinitionRecord(definition); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: "uid", Kind: "apply", At: observationTestHour}); err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(observationLog.root, observationDefinitionsFile),
		filepath.Join(observationLog.root, "20261020T10", "management.00.jsonl"),
	}
	limit := uint64(0)
	for _, name := range files {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		limit = max(limit, uint64(info.Size())+16)
	}
	var previous syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &previous); err != nil {
		t.Fatal(err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: limit, Max: previous.Max}); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("x", 4096)
	definitionErr := observationLog.DefinitionRecord(&Definition{Name: large, UID: "uid", Generation: 2})
	managementErr := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: "uid", Kind: "apply", Error: large, At: observationTestHour})
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &previous); err != nil {
		t.Fatal(err)
	}
	if definitionErr == nil || managementErr == nil {
		t.Fatalf("over-limit appends succeeded: %v %v", definitionErr, managementErr)
	}
	if err := observationLog.DefinitionRecord(&Definition{Name: "workload", UID: "uid", Generation: 3}); err != nil {
		t.Fatal(err)
	}
	if err := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: "uid", Kind: "apply", At: observationTestHour}); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
		for _, line := range lines {
			if !json.Valid(line) {
				t.Fatalf("%s kept a partial append: %q", name, line)
			}
		}
		if len(lines) != 2 {
			t.Fatalf("%s has %d lines", name, len(lines))
		}
	}
}
