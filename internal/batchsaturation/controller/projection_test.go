package controller

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestProjectSnapshotReadsValuesFromImmutableRunFieldsNotLabels(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 2,
	}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	run.Labels[labelBatch] = "99"
	run.Labels[labelAttempt] = "99"
	run.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{
		Verdict: "Accepted", PolicyVersion: "v1",
	}
	snapshot, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run}, []int{1, 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snapshot.Trials) != 1 || snapshot.Trials[0].Measurement.Batch != 2 ||
		snapshot.Trials[0].Attempt != 1 {
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

func TestTerminallyInvalidRunsExhaustTheExistingRetryPath(t *testing.T) {
	parent := testParent()
	parent.Spec.RetryLimit = 1
	measurement := batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 1,
	}
	runs := make([]edgev1alpha1.ProfilingRun, 0, 2)
	for attempt := 1; attempt <= 2; attempt++ {
		run, err := BuildRun(parent, measurement, attempt)
		if err != nil {
			t.Fatalf("build attempt %d: %v", attempt, err)
		}
		run.Status.Conditions = []metav1.Condition{{
			Type:   edgev1alpha1.ProfilingRunConditionEvidenceVerified,
			Status: metav1.ConditionFalse, Reason: "EvidenceInvalid",
		}}
		runs = append(runs, *run)
	}

	snapshot, err := ProjectSnapshot(parent, runs, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := batchsaturation.Decide(snapshot, terminalTrialMethod{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != batchsaturation.ActionFail || decision.Reason != "RetryLimitExhausted" {
		t.Fatalf("decision = %#v, want Fail/RetryLimitExhausted", decision)
	}
	status := deriveStatus(parent.Generation, snapshot, decision, nil, nil)
	if status.Phase != batchSaturationPhaseFailed || status.Outcome != "RetryLimitExhausted" {
		t.Fatalf("campaign status = %#v, want Failed/RetryLimitExhausted", status)
	}
}

type terminalTrialMethod struct{}

func (terminalTrialMethod) Plan(batchsaturation.Observed) (batchsaturation.Plan, error) {
	return batchsaturation.Plan{Result: &batchsaturation.Result{Outcome: "unused"}}, nil
}
