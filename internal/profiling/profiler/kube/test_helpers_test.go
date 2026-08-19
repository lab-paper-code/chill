package kube

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiling/run"
)

const (
	testSystemNamespace = "chill-system"
	testFactsUID        = "facts-uid"
)

func currentIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	payload, err := os.ReadFile("../testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
	return intent
}
