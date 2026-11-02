package controller

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestProjectSnapshotReadsValuesFromImmutableRunFieldsNotLabels(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}, 1, "ort-cpu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	run.Labels[labelBatch] = "99"
	run.Labels[labelAttempt] = "99"

	run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{Store: "evidence", Key: "run/final", Size: 1}
	snapshot, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run}, nil, []int{1, 2}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snapshot.Trials) != 1 || snapshot.Trials[0].Measurement.Batch != 2 ||
		snapshot.Trials[0].Attempt != 1 {
		t.Fatalf("projection trusted the label instead of the spec: %+v", snapshot.Trials)
	}
	if snapshot.Trials[0].Verdict != batchenergyoptimization.VerdictCompleted {
		t.Fatalf("verdict = %q", snapshot.Trials[0].Verdict)
	}
}

func TestProjectSnapshotRejectsAForeignRun(t *testing.T) {
	parent := testParent()
	run, _ := BuildRun(parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1,
	}, 1, "ort-cpu")
	run.OwnerReferences[0].UID = "someone-else"
	if _, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run}, nil, []int{1}, false); err == nil {
		t.Fatal("a Run owned by another object was completed")
	}
}

func TestTerminallyInvalidRunsExhaustTheExistingRetryPath(t *testing.T) {
	parent := testParent()
	parent.Spec.RetryLimit = 1
	measurement := batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1,
	}
	runs := make([]edgev1alpha1.ProfilingRun, 0, 2)
	for attempt := 1; attempt <= 2; attempt++ {
		run, err := BuildRun(parent, measurement, attempt, "ort-cpu")
		if err != nil {
			t.Fatalf("build attempt %d: %v", attempt, err)
		}
		run.Status.Conditions = []metav1.Condition{{
			Type:   edgev1alpha1.ProfilingRunConditionEvidenceVerified,
			Status: metav1.ConditionFalse, Reason: "EvidenceInvalid",
		}}
		runs = append(runs, *run)
	}

	snapshot, err := ProjectSnapshot(parent, runs, nil, []int{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := batchenergyoptimization.Decide(snapshot, terminalTrialMethod{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != batchenergyoptimization.ActionFail || decision.Reason != "RetryLimitExhausted" {
		t.Fatalf("decision = %#v, want Fail/RetryLimitExhausted", decision)
	}
	status := deriveStatus(parent.Generation, snapshot, decision, nil, nil)
	if status.Phase != batchEnergyOptimizationPhaseFailed || status.Outcome != "RetryLimitExhausted" {
		t.Fatalf("campaign status = %#v, want Failed/RetryLimitExhausted", status)
	}
}

func TestRunOutcomeDrivesCampaignTransition(t *testing.T) {
	for _, test := range []struct {
		name                  string
		evidence              bool
		conditionType, reason string
		verdict               batchenergyoptimization.Verdict
	}{
		{"verified evidence wins", true, "ExecutionComplete", "JobFailed", batchenergyoptimization.VerdictCompleted},
		{"running", false, "ExecutionComplete", "JobRunning", batchenergyoptimization.VerdictPending},
		{"unrelated condition", false, "Unrelated", "JobFailed", batchenergyoptimization.VerdictPending},
		{"failed execution", false, "ExecutionComplete", "RequiredStateNotConfirmed", batchenergyoptimization.VerdictFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := testParent()
			run, err := BuildRun(parent, batchenergyoptimization.Measurement{
				WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1,
			}, 1, "ort-cpu")
			if err != nil {
				t.Fatal(err)
			}
			run.Generation = 2
			run.Status.Conditions = []metav1.Condition{{
				Type: test.conditionType, Status: metav1.ConditionFalse, Reason: test.reason,
				ObservedGeneration: 1,
			}}
			if test.evidence {
				run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{Key: "verified"}
			}
			snapshot, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run}, nil, []int{1}, false)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Trials[0].Verdict != test.verdict {
				t.Fatalf("trial = %+v", snapshot.Trials[0])
			}
			decision, err := batchenergyoptimization.Decide(snapshot, terminalTrialMethod{})
			if err != nil {
				t.Fatal(err)
			}
			if test.verdict == batchenergyoptimization.VerdictPending && decision.Action != batchenergyoptimization.ActionWait {
				t.Fatalf("pending Run advanced campaign: %+v", decision)
			}
			if test.verdict == batchenergyoptimization.VerdictFailed && decision.Action != batchenergyoptimization.ActionCreateRun {
				t.Fatalf("failed attempt did not retry: %+v", decision)
			}
		})
	}
}

type terminalTrialMethod struct{}

func (terminalTrialMethod) Plan(batchenergyoptimization.Observed) (batchenergyoptimization.Plan, error) {
	return batchenergyoptimization.Plan{Result: &batchenergyoptimization.Result{Outcome: "unused"}}, nil
}
