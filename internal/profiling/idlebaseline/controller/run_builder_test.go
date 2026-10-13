package controller

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildRunCreatesOnlyOneModelLoadedIdleExecution(t *testing.T) {
	parent := testBaselineParent()
	run, err := buildRun(parent, 2)
	if err != nil {
		t.Fatal(err)
	}
	measurement := run.Spec.Measurement
	if run.Name != "idle-baseline-idle-a2" || run.Spec.MeasurementStateSpec != parent.Spec.State ||
		measurement.WorkloadMode != idleWorkloadMode || measurement.Batch != parent.Spec.Batch ||
		measurement.WarmupIterations != 0 || measurement.SettlingDurationSeconds == nil ||
		*measurement.SettlingDurationSeconds != parent.Spec.Protocol.SettlingDurationSeconds ||
		measurement.CPUQuota == nil || measurement.CPUQuota.Cmp(parent.Spec.CPUQuota) != 0 ||
		measurement.InputGenerator != idleInputGenerator || measurement.OutputValidation != idleOutputValidation ||
		len(run.OwnerReferences) != 1 || run.OwnerReferences[0].Kind != "ModelLoadedIdleBaseline" ||
		run.OwnerReferences[0].UID != parent.UID {
		t.Fatalf("idle child Run drifted: %#v", run)
	}
}

func TestBuildRunUsesExactLongestParentNameAndUIDLabel(t *testing.T) {
	parent := testBaselineParent()
	parent.Name = strings.Repeat("a", 243)
	run, err := buildRun(parent, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Name) != 253 || run.Labels[labelIdleBaselineUID] != string(parent.UID) {
		t.Fatalf("long child identity = name length %d labels %#v", len(run.Name), run.Labels)
	}
}

func TestBuildRunCopiesSourceAffinity(t *testing.T) {
	parent := testBaselineParent()
	parent.Spec.CPUAffinity = []int32{31, 8}
	run, err := buildRun(parent, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(run.Spec.Measurement.CPUAffinity, parent.Spec.CPUAffinity) {
		t.Fatal("owned idle Run lost source affinity")
	}
	run.Spec.Measurement.CPUAffinity[0] = 99
	if parent.Spec.CPUAffinity[0] != 31 {
		t.Fatal("owned idle Run affinity aliases the source")
	}
}
