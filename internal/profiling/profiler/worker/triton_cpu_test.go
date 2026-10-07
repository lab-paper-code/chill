package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTritonAdapterRejectsLegacyORTBeforeExecuting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	base := newTestORTCPURuntime(t, path)
	runtime, err := NewTritonCPURuntime(base.config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Measure(context.Background(), runtimeRequest(testRunConfig(t)))
	if err == nil || !strings.Contains(err.Error(), "unsupported runtime path") {
		t.Fatalf("legacy ORT path was not rejected: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy path reached the native process: %v", err)
	}
}
