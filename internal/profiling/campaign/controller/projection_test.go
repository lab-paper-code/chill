package controller

import (
	"errors"
	"fmt"
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
	run.Name = "opaque-trial-name"

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
	_, err := ProjectSnapshot(parent, []edgev1alpha1.ProfilingRun{*run}, nil, []int{1}, false)
	if err == nil || errors.Is(err, errInvalidCampaignTrial) {
		t.Fatalf("foreign owner must remain a reconciliation error, got %v", err)
	}
}

func TestTerminallyInvalidRunsFailWithoutRetry(t *testing.T) {
	parent := testParent()
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
	if decision.Action != batchenergyoptimization.ActionFail || decision.Reason != "FatalFailure" {
		t.Fatalf("decision = %#v, want Fail/FatalFailure", decision)
	}
	status := deriveStatus(parent.Generation, snapshot, decision, nil, nil)
	if status.Phase != batchEnergyOptimizationPhaseFailed || status.Outcome != "FatalFailure" {
		t.Fatalf("campaign status = %#v, want Failed/FatalFailure", status)
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
			if test.verdict == batchenergyoptimization.VerdictFailed && decision.Action != batchenergyoptimization.ActionFail {
				t.Fatalf("failed attempt did not fail campaign: %+v", decision)
			}
		})
	}
}

type terminalTrialMethod struct{}

func (terminalTrialMethod) Plan(batchenergyoptimization.Observed) (batchenergyoptimization.Plan, error) {
	return batchenergyoptimization.Plan{Result: &batchenergyoptimization.Result{Outcome: "unused"}}, nil
}

func TestTrialBoundaryUsesTypedActualRuntimeTermination(t *testing.T) {
	for _, test := range []struct {
		name            string
		termination     edgev1alpha1.ProfilingRunRuntimeTermination
		want            string
		evidenceInvalid bool
	}{
		{name: "runtime OOM", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Container: "runtime", Stage: "Execution", Reason: "OOMKilled"}, want: batchenergyoptimization.DiscoveryStopOOM},
		{name: "runtime allocator", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Container: "runtime", Stage: "Execution", FailureReason: "MemoryAllocationFailed", Reason: "Error", ExitCode: 3}, want: batchenergyoptimization.DiscoveryStopOOM},
		{name: "startup allocator", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "runtime", Stage: "Startup", FailureReason: "MemoryAllocationFailed"}},
		{name: "observer allocator", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "metric-observer", Stage: "Execution", FailureReason: "MemoryAllocationFailed"}},
		{name: "infrastructure allocator", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "RetryableFailure", Container: "runtime", Stage: "Execution", FailureReason: "MemoryAllocationFailed", PodReason: "Evicted"}},
		{name: "startup runtime OOM", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "runtime", Stage: "Startup", Reason: "OOMKilled"}},
		{name: "runtime error", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "FatalFailure", Container: "runtime", Stage: "Execution", FailureReason: "ExecutionFailed", ExitCode: 3}},
		{name: "runtime budget", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Container: "runtime", Stage: "Execution", FailureReason: "MeasurementBudgetExceeded"}, want: batchenergyoptimization.DiscoveryStopMeasurementBudgetExceeded},
		{name: "native deadline", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Container: "runtime", Stage: "Execution", JobReason: "DeadlineExceeded"}, want: batchenergyoptimization.DiscoveryStopMeasurementBudgetExceeded},
		{name: "observer OOM", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "metric-observer", Stage: "Execution", Reason: "OOMKilled"}},
		{name: "integrity failure", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "FatalFailure", Container: "runtime", Stage: "ResultValidation", FailureReason: "ExecutionFailed", ExitCode: 3}},
		{name: "commit runtime OOM", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "runtime", Stage: "EvidenceCommit", Reason: "OOMKilled"}},
		{name: "exit 137 alone", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Container: "runtime", Stage: "Execution", ExitCode: 137}},
		{name: "untyped legacy logs", termination: edgev1alpha1.ProfilingRunRuntimeTermination{ExitCode: 3, LogTail: "OOMKilled MeasurementBudgetExceeded ExecutionFailed"}},
		{name: "integrity overrides stale stop", evidenceInvalid: true, termination: edgev1alpha1.ProfilingRunRuntimeTermination{Class: "ExpectedStop", Container: "runtime", Stage: "Execution", Reason: "OOMKilled"}},
		{name: "unclassified native OOM", termination: edgev1alpha1.ProfilingRunRuntimeTermination{Container: "runtime", Stage: "Execution", Reason: "OOMKilled"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := testParent()
			run, err := BuildRun(parent, batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 4}, 2, "ort-cpu")
			if err != nil {
				t.Fatal(err)
			}
			run.UID = "actual-run-uid"
			run.Status.Conditions = []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed"}}
			if test.evidenceInvalid {
				run.Status.Conditions = append(run.Status.Conditions, metav1.Condition{
					Type: edgev1alpha1.ProfilingRunConditionEvidenceVerified, Status: metav1.ConditionFalse, Reason: "EvidenceInvalid",
				})
			}
			if test.want != "" {
				test.termination.Class = "ExpectedStop"
			}
			run.Status.RuntimeTermination = &test.termination
			trial, err := projectTrial(run)
			if err != nil {
				t.Fatal(err)
			}
			if trial.Verdict != batchenergyoptimization.VerdictFailed {
				t.Fatalf("trial=%+v", trial)
			}
			if test.want == "" {
				if trial.Boundary != nil {
					t.Fatalf("genuine failure became boundary: %+v", trial.Boundary)
				}
				if test.evidenceInvalid && trial.FailureClass != batchenergyoptimization.FailureClassFatalFailure {
					t.Fatalf("integrity failure lost fatal precedence: %+v", trial)
				}
				return
			}
			if trial.Boundary == nil || trial.Boundary.Reason != test.want ||
				trial.Boundary.Run == nil || trial.Boundary.Run.UID != string(run.UID) ||
				trial.Boundary.Attempt != 2 || trial.Boundary.FailedMeasurement.Batch != 4 ||
				trial.Boundary.Diagnostic == "" {
				t.Fatalf("actual termination lost: %+v", trial.Boundary)
			}
		})
	}
}

func TestInfrastructureRetriesSameBatchWithinFailureBudget(t *testing.T) {
	for _, failures := range []int{1, 2} {
		t.Run(fmt.Sprintf("failures-%d", failures), func(t *testing.T) {
			parent := testParent()
			parent.Spec.RetryLimit = 1
			var runs []edgev1alpha1.ProfilingRun
			appendRun := func(mode batchenergyoptimization.WorkloadMode, attempt int, completed bool) {
				t.Helper()
				run, err := BuildRun(parent, batchenergyoptimization.Measurement{WorkloadMode: mode, Batch: 1}, attempt, "ort-cpu")
				if err != nil {
					t.Fatal(err)
				}
				if completed {
					run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{Store: "evidence", Key: run.Name, Size: 1}
				} else {
					run.Status.Conditions = []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed"}}
					run.Status.RuntimeTermination = &edgev1alpha1.ProfilingRunRuntimeTermination{
						Class: "RetryableFailure", Container: "runtime", Stage: "Execution",
						Reason: "OOMKilled", FailureReason: "Evicted", PodReason: "Evicted", PodMessage: "node memory pressure", ExitCode: 137,
					}
				}
				runs = append(runs, *run)
			}
			for attempt := 1; attempt <= 3; attempt++ {
				appendRun(batchenergyoptimization.WorkloadModeIdle, attempt, true)
			}
			for attempt := 1; attempt <= 2; attempt++ {
				appendRun(batchenergyoptimization.WorkloadModeActive, attempt, true)
			}
			for failure := range failures {
				appendRun(batchenergyoptimization.WorkloadModeActive, 3+failure, false)
			}
			snapshot, err := ProjectSnapshot(parent, runs, nil, []int{1, 2}, false)
			if err != nil {
				t.Fatal(err)
			}
			method, err := batchenergyoptimization.NewQuickEnergyMinimization(batchenergyoptimization.QuickEnergyConfig{TrialsPerBatch: 3}, nil)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := batchenergyoptimization.Decide(snapshot, method)
			if err != nil {
				t.Fatal(err)
			}
			if failures > 1 {
				if decision.Action != batchenergyoptimization.ActionFail || decision.Reason != "RetryLimitExhausted" {
					t.Fatalf("infra failures escaped retry budget: %+v", decision)
				}
				return
			}
			if decision.Action != batchenergyoptimization.ActionCreateRun || decision.Run == nil ||
				decision.Run.WorkloadMode != batchenergyoptimization.WorkloadModeActive || decision.Run.Batch != 1 ||
				nextAttempt(*decision.Run, snapshot.Trials) != 4 {
				t.Fatalf("infra OOM did not retry same K after two successes: %+v", decision)
			}
			if snapshot.Trials[len(snapshot.Trials)-1].Boundary != nil {
				t.Fatal("explicit eviction became expected discovery stop")
			}
			appendRun(batchenergyoptimization.WorkloadModeActive, 4, true)
			resumed, err := ProjectSnapshot(parent, runs, nil, []int{1, 2}, false)
			if err != nil {
				t.Fatal(err)
			}
			decision, err = batchenergyoptimization.Decide(resumed, method)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Action != batchenergyoptimization.ActionCreateRun || decision.Run == nil || decision.Run.Batch != 2 {
				t.Fatalf("successful retry did not advance after three actual successes: %+v", decision)
			}
		})
	}
}
