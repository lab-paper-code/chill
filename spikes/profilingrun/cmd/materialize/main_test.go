package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
)

func TestMaterializeExactSingleAttemptJob(t *testing.T) {
	intent := testIntent(t)
	objects, err := materialize(intent, "test")
	if err != nil {
		t.Fatal(err)
	}
	job := objects[2].(*batchv1.Job)
	if *job.Spec.Completions != 1 || *job.Spec.Parallelism != 1 || *job.Spec.BackoffLimit != 0 {
		t.Fatal("job is not one exact attempt")
	}
	if job.Spec.Template.Spec.AutomountServiceAccountToken == nil ||
		*job.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("service account token must be disabled")
	}
	if len(job.Spec.Template.Spec.Containers) != 2 || len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatal("unexpected container topology")
	}
	for _, container := range append(job.Spec.Template.Spec.InitContainers, job.Spec.Template.Spec.Containers...) {
		if !strings.Contains(container.Image, "@sha256:") || container.ImagePullPolicy != "IfNotPresent" {
			t.Fatalf("image not frozen: %s", container.Image)
		}
	}
}

func TestReadIntentStrict(t *testing.T) {
	intent := testIntent(t)
	payload, _ := json.Marshal(intent)
	path := filepath.Join(t.TempDir(), "intent.json")
	if err := os.WriteFile(path, append(payload, []byte(` {}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readIntent(path); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}

func testIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	path := filepath.Join("..", "..", "fixtures", "lattepanda-ort-cpu-bs1.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
	return intent
}
