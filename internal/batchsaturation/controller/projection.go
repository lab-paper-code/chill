package controller

import (
	"errors"
	"fmt"
	"strconv"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectSnapshot verifies ownership and reads every value from the child spec.
// Labels select Runs; they are never the source of a projected value.
func ProjectSnapshot(
	parent *edgev1alpha1.BatchSaturation,
	runs []edgev1alpha1.ProfilingRun,
	eligibleBatches []int,
) (batchsaturation.Snapshot, error) {
	if parent == nil {
		return batchsaturation.Snapshot{}, errors.New("a parent BatchSaturation is required")
	}
	snapshot := batchsaturation.Snapshot{
		RetryLimit:      int(parent.Spec.RetryLimit),
		EligibleBatches: append([]int(nil), eligibleBatches...),
	}
	for index := range runs {
		run := &runs[index]
		owner := metav1.GetControllerOf(run)
		if owner == nil || owner.Kind != "BatchSaturation" ||
			owner.APIVersion != edgev1alpha1.GroupVersion.String() ||
			string(owner.UID) != string(parent.UID) {
			return batchsaturation.Snapshot{}, fmt.Errorf(
				"ProfilingRun %q is not controller-owned by this BatchSaturation", run.Name,
			)
		}
		trial, err := projectTrial(run)
		if err != nil {
			return batchsaturation.Snapshot{}, fmt.Errorf("project ProfilingRun %q: %w", run.Name, err)
		}
		snapshot.Trials = append(snapshot.Trials, trial)
	}
	if err := snapshot.Validate(); err != nil {
		return batchsaturation.Snapshot{}, fmt.Errorf("validate projected snapshot: %w", err)
	}
	return snapshot, nil
}

func projectTrial(run *edgev1alpha1.ProfilingRun) (batchsaturation.Trial, error) {
	mode := batchsaturation.WorkloadModeActive
	if run.Spec.Measurement.WorkloadMode == idleWorkloadMode {
		mode = batchsaturation.WorkloadModeIdle
	}
	attempt, err := strconv.Atoi(run.Labels[labelAttempt])
	if err != nil || attempt < 1 {
		return batchsaturation.Trial{}, errors.New("owned Run has no usable attempt label")
	}
	trial := batchsaturation.Trial{
		Measurement: batchsaturation.Measurement{
			WorkloadMode: mode, Batch: int(run.Spec.Measurement.Batch),
		},
		Attempt: attempt,
		Verdict: batchsaturation.VerdictPending,
	}
	if run.Status.Admission != nil {
		if run.Status.Admission.Verdict == "Accepted" {
			trial.Verdict = batchsaturation.VerdictAccepted
			return trial, nil
		}
		trial.Verdict = batchsaturation.VerdictFailed
		return trial, nil
	}
	for _, condition := range run.Status.Conditions {
		if condition.Type != "IntentFrozen" && condition.Type != "ExecutionComplete" {
			continue
		}
		if condition.Status != metav1.ConditionFalse || condition.Reason == "" {
			continue
		}
		if isTerminalReason(condition.Reason) {
			trial.Verdict = batchsaturation.VerdictFailed
			return trial, nil
		}
	}
	return trial, nil
}

func isTerminalReason(reason string) bool {
	switch reason {
	case "InvalidRunSpec",
		"AdmissionPolicyInvalid",
		"ExecutionPolicyInvalid",
		"ActuationPolicyInvalid",
		"ExecutionLost",
		"JobFailed",
		"NodeActuationFailed",
		"NodeRestoreFailed",
		"NodeActuationEndedBeforeExecution",
		"NodeActuationInterruptedExecution",
		"NodeActuationOrderingInvalid":
		return true
	default:
		return false
	}
}
