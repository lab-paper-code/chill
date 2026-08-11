package controller

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
)

func TestProjectSnapshotReadsValuesFromTheRunSpecNotLabels(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	run.Labels[labelBatch] = "99"
	run.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Accepted", PolicyVersion: "v1",
	}
	snapshot, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run}, []int{1, 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snapshot.Trials) != 1 || snapshot.Trials[0].Measurement.Batch != 2 {
		t.Fatalf("projection trusted the label instead of the spec: %+v", snapshot.Trials)
	}
	if snapshot.Trials[0].Verdict != batchsaturation.VerdictAccepted {
		t.Fatalf("verdict = %q", snapshot.Trials[0].Verdict)
	}
}

func TestProjectSnapshotRejectsAForeignRun(t *testing.T) {
	parent := testParent()
	run, _ := BuildRun(parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeIdle, Batch: 1,
	}, 1)
	run.OwnerReferences[0].UID = "someone-else"
	if _, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run}, []int{1}); err == nil {
		t.Fatal("a Run owned by another object was accepted")
	}
}
