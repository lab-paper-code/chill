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
	path := t.TempDir() + "/./workload-result.json"
	result := validWorkloadResult()
	background := workloadobserver.Pod{
		Namespace: "monitoring", Name: "agent", UID: "agent-uid", Phase: "Running",
		Images: []string{"registry.example/agent"},
	}
	result.InitialActivePods[0].Images = []string{"registry.example/z", "registry.example/a", "registry.example/z"}
	result.InitialActivePods[0].Labels = map[string]string{}
	result.InitialActivePods = append(result.InitialActivePods, background)
	result.FinalActivePods = []workloadobserver.Pod{background, result.InitialActivePods[0]}
	result.FinalActivePods[1].Labels = nil
	result.FinalActivePods[1].Images = []string{"registry.example/a", "registry.example/z"}
	result.Events = []workloadobserver.Event{{
		Kind: workloadobserver.EventUpdated, ObservedAt: result.CoverageStartedAt.Add(time.Millisecond),
		ResourceVersion: "11", Pod: result.InitialActivePods[0],
	}}
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
	if len(record.InitialActivePods) != 2 || len(record.FinalActivePods) != 2 ||
		record.InitialActivePods[0].UID != record.FinalActivePods[1].UID ||
		record.InitialActivePods[1].UID != record.FinalActivePods[0].UID {
		t.Fatalf("observed membership changed: initial=%+v final=%+v", record.InitialActivePods, record.FinalActivePods)
	}
	if len(record.Events) != 1 || record.Events[0].Kind != workloadobserver.EventUpdated ||
		!workloadobserver.SamePod(record.Events[0].Pod, record.InitialActivePods[0]) ||
		record.Events[0].ResourceVersion != record.LastResourceVersion {
		t.Fatalf("observed Pod update changed: %+v", record.Events)
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

func TestWorkloadResultFileRejectsMalformedObservation(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*workloadobserver.Result)
	}{
		{name: "continuity", alter: func(result *workloadobserver.Result) { result.Continuous = false }},
		{name: "identity", alter: func(result *workloadobserver.Result) { result.InitialActivePods[0].UID = "" }},
		{name: "duplicate UID", alter: func(result *workloadobserver.Result) {
			result.FinalActivePods = append(result.FinalActivePods, result.FinalActivePods[0])
		}},
		{name: "wrong event target", alter: func(result *workloadobserver.Result) {
			pod := result.InitialActivePods[0]
			pod.Name = "another-pod"
			result.Events = []workloadobserver.Event{{
				Kind: workloadobserver.EventUpdated, ObservedAt: result.CoverageStartedAt,
				ResourceVersion: "11", Pod: pod,
			}}
		}},
		{name: "event chronology", alter: func(result *workloadobserver.Result) {
			result.Events = []workloadobserver.Event{
				{
					Kind: workloadobserver.EventUpdated, ObservedAt: result.CoverageEndedAt,
					ResourceVersion: "11", Pod: result.InitialActivePods[0],
				},
				{
					Kind: workloadobserver.EventUpdated, ObservedAt: result.CoverageStartedAt,
					ResourceVersion: "12", Pod: result.InitialActivePods[0],
				},
			}
		}},
		{name: "event limit", alter: func(result *workloadobserver.Result) { result.EventLimit = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			result := validWorkloadResult()
			test.alter(&result)
			if err := PublishWorkloadResult(path, 1<<20, result); err != nil {
				t.Fatal(err)
			}
			reader, err := NewWorkloadResultFile(WorkloadResultFileConfig{
				Path: path, WaitTimeout: time.Second, PollInterval: time.Millisecond, MaxBytes: 1 << 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Read(context.Background()); err == nil {
				t.Fatal("reader accepted malformed observation")
			}
		})
	}
}

func TestWorkloadResultHandoffRejectsOversizedPayload(t *testing.T) {
	t.Run("publisher", func(t *testing.T) {
		err := PublishWorkloadResult(filepath.Join(t.TempDir(), "result.json"), 1, validWorkloadResult())
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("PublishWorkloadResult() error = %v", err)
		}
	})
	t.Run("reader", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "result.json")
		if err := PublishWorkloadResult(path, 1<<20, validWorkloadResult()); err != nil {
			t.Fatal(err)
		}
		reader, err := NewWorkloadResultFile(WorkloadResultFileConfig{
			Path: path, WaitTimeout: time.Second, PollInterval: time.Millisecond, MaxBytes: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Read(context.Background()); err == nil {
			t.Fatal("reader accepted oversized handoff")
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
		CoverageStartedAt: started, CoverageEndedAt: started.Add(time.Second),
		InitialResourceVersion: "10", LastResourceVersion: "11", EventLimit: 10,
		InitialActivePods: []workloadobserver.Pod{pod}, FinalActivePods: []workloadobserver.Pod{pod},
		Continuous: true,
	}
}
