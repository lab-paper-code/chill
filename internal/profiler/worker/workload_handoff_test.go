package worker

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

func TestWorkloadResultFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workload-result.json")
	result := validWorkloadResult()
	if err := PublishWorkloadResult(path, 1<<20, result); err != nil {
		t.Fatal(err)
	}
	reader, err := NewWorkloadResultFile(WorkloadResultFileConfig{
		Path: path, WaitTimeout: time.Second, PollInterval: time.Millisecond, MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, err := got.Record()
	if err != nil {
		t.Fatal(err)
	}
	if record.LastResourceVersion != result.LastResourceVersion || !record.Continuous {
		t.Fatalf("workload result = %#v", got)
	}
	record.InitialActivePods[0].Name = mutatedCopyName
	sealed, err := got.Record()
	if err != nil {
		t.Fatal(err)
	}
	if sealed.InitialActivePods[0].Name != result.InitialActivePods[0].Name {
		t.Fatalf("sealed workload result was changed through a detached record: %#v", sealed)
	}
}

func TestPublishWorkloadResultFailsClosed(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		result := validWorkloadResult()
		result.Continuous = false
		err := PublishWorkloadResult(filepath.Join(t.TempDir(), "result.json"), 1<<20, result)
		if err == nil || !strings.Contains(err.Error(), "continuity") {
			t.Fatalf("PublishWorkloadResult() error = %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		err := PublishWorkloadResult(filepath.Join(t.TempDir(), "result.json"), 1, validWorkloadResult())
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("PublishWorkloadResult() error = %v", err)
		}
	})
}

func validWorkloadResult() workloadobserver.Result {
	started := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	pod := workloadobserver.Pod{
		Namespace: "chill-system", Name: "run", UID: "pod-uid", Phase: "Running",
		Images: []string{"registry.example/runtime@sha256:abc"},
	}
	return workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target:        workloadobserver.TargetIdentity{NodeName: "lattepanda", NodeUID: "node-uid"},
		Run: workloadobserver.RunIdentity{
			Namespace: "chill-system", JobUID: "job-uid", PodName: "run", PodUID: "pod-uid",
		},
		PolicyDigest:      "sha256:" + strings.Repeat("a", 64),
		CoverageStartedAt: started, CoverageEndedAt: started.Add(time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{pod}, FinalActivePods: []workloadobserver.Pod{pod},
		Continuous: true,
	}
}
