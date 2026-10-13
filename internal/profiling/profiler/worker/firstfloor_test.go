package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstfloorAdapterRejectsReferenceORTConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	base := newTestORTCPURuntime(t, path)
	_, err := NewFirstfloorRuntime(base.config, runtimeRequest(testRunConfig(t)).RunConfig)
	if err == nil || !strings.Contains(err.Error(), "verified firstfloor run") {
		t.Fatalf("reference ORT path was not rejected: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reference path reached the native process: %v", err)
	}
}
