package profiler

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestDecideSelectsOneBoundedNextAction(t *testing.T) {
	config := testRunConfig(t)
	evidenceReference := testEvidenceReference()
	tests := []struct {
		name     string
		snapshot Snapshot
		want     Action
	}{
		{name: "deletion stops new work", snapshot: Snapshot{Deleting: true}, want: ActionStop},
		{name: "create first Job", snapshot: Snapshot{RunConfig: &config}, want: ActionCreateJob},
		{
			name: "wait for active Job",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobActive,
			},
			want: ActionWaitForJob,
		},
		{
			name: "finalize successful execution",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobSucceeded,
			},
			want: ActionFinalizeEvidence,
		},
		{
			name: "complete after evidence is terminally invalid",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobSucceeded,
				EvidenceInvalid: true,
			},
			want: ActionComplete,
		},
		{
			name: "report failed execution without retry",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobFailed,
			},
			want: ActionReportExecutionFailure,
		},
		{
			name: "report lost recorded execution without recreation",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobMissing,
			},
			want: ActionReportExecutionLost,
		},
		{
			name: "admit verified evidence after Job deletion",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobMissing,
				EvidenceReference: &evidenceReference,
			},
			want: ActionComplete,
		},
		{
			name: "complete with stored evidence",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobSucceeded,
				EvidenceReference: &evidenceReference,
			},
			want: ActionComplete,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := Decide(test.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Action != test.want {
				t.Fatalf("expected %s, got %s", test.want, decision.Action)
			}
		})
	}
}

func TestDecideRejectsContradictoryFacts(t *testing.T) {
	config := testRunConfig(t)
	evidenceReference := testEvidenceReference()
	tests := []struct {
		name     string
		snapshot Snapshot
		match    string
	}{
		{
			name:     "Job without RunConfig",
			snapshot: Snapshot{JobReference: "job-uid", Job: JobActive},
			match:    "RunConfig is required",
		},
		{
			name:     "observation without reference",
			snapshot: Snapshot{RunConfig: &config, Job: JobActive},
			match:    "without a recorded Job reference",
		},
		{
			name:     "reference without observation",
			snapshot: Snapshot{RunConfig: &config, JobReference: "job-uid"},
			match:    "requires a supported observation",
		},
		{
			name: "evidence before Job",
			snapshot: Snapshot{
				RunConfig: &config, EvidenceReference: &evidenceReference,
			},
			match: "without a recorded Job reference",
		},
		{
			name: "evidence contradicts active Job",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobActive,
				EvidenceReference: &evidenceReference,
			},
			match: "contradicts",
		},
		{
			name: "invalid evidence identity",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobSucceeded,
				EvidenceReference: &evidencestore.ObjectIdentity{Store: "store"},
			},
			match: "validate evidence reference",
		},
		{
			name: "terminal evidence invalidity contradicts active Job",
			snapshot: Snapshot{
				RunConfig: &config, JobReference: "job-uid", Job: JobActive,
				EvidenceInvalid: true,
			},
			match: "contradicts observed Job outcome",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decide(test.snapshot)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestDecideIsDeterministicAndDoesNotRetainCallerPointers(t *testing.T) {
	config := testRunConfig(t)
	snapshot := Snapshot{RunConfig: &config}
	before := config
	first, err := Decide(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Decide(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(config, before) {
		t.Fatal("decision is non-deterministic or mutated the input")
	}
	config.State.Model = "changed-after-decision"
	if first.RunConfig == nil || first.RunConfig.State.Model == config.State.Model {
		t.Fatal("decision retained the caller's config pointer")
	}
}

func testRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func testEvidenceReference() evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{
		Store: "chill-evidence",
		Key:   "runs/run-uid/execution.json",
		Size:  100,
	}
}
