package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestDiagnosticOptionsRequireExplicitFiniteServingCapture(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  options
		valid bool
	}{
		{"disabled", options{}, true},
		{"finite", options{
			diagnosticDirectory:   "/private/capture",
			diagnosticStartDelay:  time.Second,
			diagnosticTraceOffset: 60 * time.Second,
		}, true},
		{"last allowed trace", options{
			diagnosticDirectory:   "/private/capture",
			diagnosticTraceOffset: 390 * time.Second,
		}, true},
		{"timing without directory", options{diagnosticTraceOffset: time.Second}, false},
		{"relative directory", options{diagnosticDirectory: "capture"}, false},
		{"negative delay", options{diagnosticDirectory: "/private/capture", diagnosticStartDelay: -time.Second}, false},
		{"trace past window", options{
			diagnosticDirectory:   "/private/capture",
			diagnosticTraceOffset: 391 * time.Second,
		}, false},
		{"materializer", options{diagnosticDirectory: "/private/capture", materializeProfile: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.opts.validateDiagnostics(); (err == nil) != tc.valid {
				t.Fatalf("diagnostic configuration valid=%t: %v", tc.valid, err)
			}
		})
	}
}

func TestDiagnosticTraceWriterNeverExceedsItsCap(t *testing.T) {
	writer := &cappedDiagnosticTrace{data: make([]byte, 8), limit: make(chan struct{})}
	for _, data := range [][]byte{[]byte("abcdef"), []byte("ghijk"), []byte("lm")} {
		if n, err := writer.Write(data); n != len(data) || err != nil {
			t.Fatalf("trace producer could not finish: n=%d error=%v", n, err)
		}
	}
	if writer.used != 8 || writer.attempted != 13 || !writer.truncated || !bytes.Equal(writer.data, []byte("abcdefgh")) {
		t.Fatalf("trace loss/cap hidden: used=%d attempted=%d truncated=%t data=%q",
			writer.used, writer.attempted, writer.truncated, writer.data)
	}
	select {
	case <-writer.limit:
	default:
		t.Fatal("trace cap did not notify its independent stop owner")
	}
}

func TestCanceledDiagnosticCaptureWritesPrivateExplicitArtifacts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	directory := filepath.Join(t.TempDir(), "capture")
	session, err := newDiagnosticSession(ctx, options{diagnosticDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if session.recorder.Active() || session.traceBuffer.data != nil {
		t.Fatal("canceled finite capture retained active work or the trace buffer")
	}
	for _, name := range []string{"events.bin", "runtime.trace", "metadata.json"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private diagnostic artifact %s: %v %v", name, info, err)
		}
		if name != "metadata.json" && info.Size() != 0 {
			t.Fatalf("inactive capture emitted %s", name)
		}
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("capture directory is not private", err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		StopReason     string                     `json:"stopReason"`
		Numeric        serving.DiagnosticMetadata `json:"numeric"`
		Trace          diagnosticTraceMetadata    `json:"trace"`
		EventFileBytes int64                      `json:"eventFileBytes"`
	}
	if err = json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.StopReason != "canceled_before_listener" || metadata.Numeric.Activated ||
		metadata.Numeric.Retained != 0 || metadata.EventFileBytes != 0 || metadata.Trace.StopReason != "not_started" ||
		metadata.Trace.CapacityBytes != diagnosticTraceBytes || metadata.Trace.DurationLimitNanos != int64(30*time.Second) {
		t.Fatalf("inactive/finite metadata was lost: %+v", metadata)
	}
	if _, err = newDiagnosticSession(context.Background(), options{diagnosticDirectory: directory}); err == nil {
		t.Fatal("existing capture directory was reused")
	}
}
