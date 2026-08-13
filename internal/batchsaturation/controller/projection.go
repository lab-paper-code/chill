package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectSnapshot verifies ownership and projects each child. Labels select
// candidate children; projected measurement and attempt values come from the
// immutable Run spec and name.
func ProjectSnapshot(
	parent *edgev1alpha1.BatchSaturation,
	runs []edgev1alpha1.ProfilingRun,
	plannedBatchSizes []int,
) (batchsaturation.Snapshot, error) {
	if parent == nil {
		return batchsaturation.Snapshot{}, errors.New("a parent BatchSaturation is required")
	}
	snapshot := batchsaturation.Snapshot{
		RetryLimit:        int(parent.Spec.RetryLimit),
		PlannedBatchSizes: append([]int(nil), plannedBatchSizes...),
	}
	for index := range runs {
		run := &runs[index]
		trial, err := projectOwnedTrial(parent, run)
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

func projectOwnedTrial(
	parent *edgev1alpha1.BatchSaturation,
	run *edgev1alpha1.ProfilingRun,
) (batchsaturation.Trial, error) {
	owner := metav1.GetControllerOf(run)
	if owner == nil || owner.Kind != "BatchSaturation" ||
		owner.APIVersion != edgev1alpha1.GroupVersion.String() ||
		string(owner.UID) != string(parent.UID) {
		return batchsaturation.Trial{}, errors.New("Run is not controller-owned by this BatchSaturation")
	}
	return projectTrial(parent.Name, run)
}

func projectTrial(parentName string, run *edgev1alpha1.ProfilingRun) (batchsaturation.Trial, error) {
	mode := batchsaturation.WorkloadModeActive
	if run.Spec.Measurement.WorkloadMode == idleWorkloadMode {
		mode = batchsaturation.WorkloadModeIdle
	}
	measurement := batchsaturation.Measurement{
		WorkloadMode: mode, Batch: int(run.Spec.Measurement.Batch),
	}
	attempt, err := attemptFromRunName(parentName, measurement, run.Name)
	if err != nil {
		return batchsaturation.Trial{}, err
	}
	trial := batchsaturation.Trial{
		Measurement: measurement,
		Attempt:     attempt,
		Verdict:     batchsaturation.VerdictPending,
	}
	if run.Status.Admission != nil {
		if run.Status.Admission.Verdict == "Accepted" {
			trial.Verdict = batchsaturation.VerdictAccepted
			return trial, nil
		}
		trial.Verdict = batchsaturation.VerdictFailed
		return trial, nil
	}
	if len(terminalFailureReasons(run.Status.Conditions)) > 0 {
		trial.Verdict = batchsaturation.VerdictFailed
	}
	return trial, nil
}

func terminalFailureReasons(conditions []metav1.Condition) []string {
	reasons := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		if !isTrialProjectionConditionType(condition.Type) ||
			condition.Status != metav1.ConditionFalse || !isTerminalReason(condition.Reason) {
			continue
		}
		reasons = append(reasons, condition.Reason)
	}
	return reasons
}

func isTrialProjectionConditionType(conditionType string) bool {
	return conditionType == "IntentFrozen" || conditionType == "ExecutionComplete" ||
		conditionType == edgev1alpha1.ProfilingRunConditionEvidenceVerified
}

func isTerminalReason(reason string) bool {
	switch reason {
	case "InvalidRunSpec",
		"AdmissionPolicyInvalid",
		"ExecutionPolicyInvalid",
		"ActuationPolicyInvalid",
		"ExecutionLost",
		"JobFailed",
		"RequiredStateNotConfirmed",
		"EvidenceInvalid":
		return true
	default:
		return false
	}
}
