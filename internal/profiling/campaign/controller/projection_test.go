package controller

import (
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestProjectSnapshotReadsValuesFromImmutableRunFieldsNotLabels(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}, 1)
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
	}, 1)
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

func TestProjectSnapshotProjectsEstablishedStateFromTheOwnedTransition(t *testing.T) {
	parent := testParent()
	material := &TransitionMaterial{
		Node:        edgev1alpha1.ProfilingRunObjectReference{Name: parent.Spec.State.TargetNode, UID: "node-uid"},
		DeviceClass: parent.Spec.State.Candidate.DeviceClass,
		PowerMode:   parent.Spec.State.Candidate.PowerMode,
		Actuator: edgev1alpha1.PowerModeActuator{
			Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
			JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 4},
		},
		PolicyName: "jetson-mode", SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300,
	}
	transition, err := BuildTransition(parent, material, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	transition.Generation = 1
	readyAt := metav1.NewMicroTime(time.Unix(1100, 0))
	transition.Status = edgev1alpha1.NodeActuationStatus{
		ObservedGeneration: 1, Phase: edgev1alpha1.NodeActuationPhaseReady,
		TargetObservation: &edgev1alpha1.NodeActuationObservation{
			PowerMode: material.PowerMode, Actuator: material.Actuator,
			ObservedAt: metav1.NewMicroTime(time.Unix(1090, 0)),
		},
		ReadyAt: &readyAt,
	}

	snapshot, err := ProjectSnapshot(parent, nil, []edgev1alpha1.NodeActuation{*transition}, []int{1}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := batchenergyoptimization.State{
		TargetNode:  parent.Spec.State.TargetNode,
		DeviceClass: parent.Spec.State.Candidate.DeviceClass,
		PowerMode:   parent.Spec.State.Candidate.PowerMode,
	}
	if snapshot.RequiredState != want || snapshot.Transition == nil ||
		snapshot.Transition.Outcome != batchenergyoptimization.TransitionEstablished ||
		snapshot.Transition.EstablishedState == nil || *snapshot.Transition.EstablishedState != want {
		t.Fatalf("projected campaign state = %#v", snapshot)
	}
}

func TestRequiredStateNotConfirmedIsTerminal(t *testing.T) {
	if !isTerminalReason("RequiredStateNotConfirmed") {
		t.Fatal("RequiredStateNotConfirmed was not classified as terminal")
	}
}

type terminalTrialMethod struct{}

func (terminalTrialMethod) Plan(batchenergyoptimization.Observed) (batchenergyoptimization.Plan, error) {
	return batchenergyoptimization.Plan{Result: &batchenergyoptimization.Result{Outcome: "unused"}}, nil
}
