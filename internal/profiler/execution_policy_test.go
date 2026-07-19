package profiler

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestDeriveExecutionPlanBindsOneCoherentCPUORTEnvelope(t *testing.T) {
	intent := testIntent(t)
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/model.onnx"

	plan, err := DeriveExecutionPlan(intent)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Policy != CPUORTWorkerPolicy || plan.PolicyVersion != CPUORTWorkerPolicyVersion {
		t.Fatalf("unexpected policy identity: %#v", plan)
	}
	if plan.MaxPowerEvents != 33 {
		t.Fatalf("MaxPowerEvents = %d, want 33", plan.MaxPowerEvents)
	}
	if plan.ActiveDeadline != 211*time.Second || plan.AttemptTimeout != 181*time.Second {
		t.Fatalf("unexpected timing envelope: %#v", plan)
	}
	if plan.PowerResultTimeout != 31*time.Second || plan.ObserverReadyTimeout >= plan.AttemptTimeout ||
		plan.ObserverStartTimeout >= plan.AttemptTimeout {
		t.Fatalf("incoherent handoff timing: %#v", plan)
	}
	if plan.MaxInputBytes+plan.MaxRuntimeResultBytes >= plan.RuntimeMemoryLimitBytes ||
		plan.MaxPowerResultBytes > plan.CoordinationBytes ||
		plan.MaxRuntimeResultBytes+plan.MaxPowerResultBytes > plan.WorkspaceBytes {
		t.Fatalf("capacities do not fit the resource envelope: %#v", plan)
	}
}

func TestDeriveExecutionPlanRejectsUnpromotedRuntimePath(t *testing.T) {
	intent := testIntent(t)
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/model.onnx"
	intent.Candidate.ExecutionPath = "tensorrt"
	if _, err := DeriveExecutionPlan(intent); err == nil {
		t.Fatal("DeriveExecutionPlan() error = nil, want unsupported path rejection")
	}
}

func TestDeriveExecutionPlanRejectsPowerScheduleOutsideBoundedEnvelope(t *testing.T) {
	intent := testIntent(t)
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = "/artifact/model.onnx"
	intent.Power.DurationSeconds = 60_000
	intent.Power.IntervalMilliseconds = 1000
	if _, err := DeriveExecutionPlan(intent); err == nil {
		t.Fatal("DeriveExecutionPlan() error = nil, want bounded event-capacity rejection")
	}
}
