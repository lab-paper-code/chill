package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstfloorAdapterRejectsNonFirstfloorConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	base := newTestFirstfloorRuntime(t, path)
	config := testRunConfig(t)
	config.State.RuntimeFamily, config.State.Backend, config.State.Firstfloor = "onnxruntime", testCPUProvider, nil
	_, err := NewFirstfloorRuntime(base.config, runtimeRequest(config).RunConfig)
	if err == nil || !strings.Contains(err.Error(), "verified firstfloor run") {
		t.Fatalf("non-firstfloor path was not rejected: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("non-firstfloor path reached the native process: %v", err)
	}
}
