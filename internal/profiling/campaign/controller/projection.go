package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var errInvalidCampaignTrial = errors.New("invalid campaign trial identity")

// ProjectSnapshot verifies ownership and projects each child. Labels select
// candidate children; measurement and attempt values come from the immutable
// Run spec. transition is the Node's projected power mode.
func ProjectSnapshot(
	parent *edgev1alpha1.BatchEnergyOptimization,
	runs []edgev1alpha1.ProfilingRun,
	transition *batchenergyoptimization.Transition,
	plannedBatchSizes []int,
	transitionRequired bool,
) (batchenergyoptimization.Snapshot, error) {
	if parent == nil {
		return batchenergyoptimization.Snapshot{}, errors.New("a parent BatchEnergyOptimization is required")
	}
	snapshot := batchenergyoptimization.Snapshot{
		RequiredState: batchenergyoptimization.State{
			TargetNode:  parent.Spec.State.TargetNode,
			DeviceClass: parent.Spec.State.Candidate.DeviceClass,
			PowerMode:   parent.Spec.State.Candidate.PowerMode,
		},
		Transition:         transition,
		TransitionRequired: transitionRequired,
		RetryLimit:         int(parent.Spec.RetryLimit),
		PlannedBatchSizes:  append([]int(nil), plannedBatchSizes...),
	}
	for index := range runs {
		run := &runs[index]
		trial, err := projectOwnedTrial(parent, run)
		if err != nil {
			return batchenergyoptimization.Snapshot{}, fmt.Errorf("project ProfilingRun %q: %w", run.Name, err)
		}
		snapshot.Trials = append(snapshot.Trials, trial)
	}
	return snapshot, nil
}

func projectOwnedTrial(
	parent *edgev1alpha1.BatchEnergyOptimization,
	run *edgev1alpha1.ProfilingRun,
) (batchenergyoptimization.Trial, error) {
	if !controlledByParent(parent, run) {
		return batchenergyoptimization.Trial{}, errors.New("Run is not controller-owned by this BatchEnergyOptimization")
	}
	return projectTrial(run)
}

// controlledByParent reports whether the parent's exact UID is the object's
// controller owner.
func controlledByParent(parent *edgev1alpha1.BatchEnergyOptimization, object metav1.Object) bool {
	owner := metav1.GetControllerOf(object)
	return owner != nil && owner.Kind == "BatchEnergyOptimization" &&
		owner.APIVersion == edgev1alpha1.GroupVersion.String() &&
		parent.UID != "" && owner.UID == parent.UID
}

func projectTrial(run *edgev1alpha1.ProfilingRun) (batchenergyoptimization.Trial, error) {
	identity := run.Spec.CampaignTrial
	if identity == nil || identity.Batch < 1 || identity.Attempt < 1 {
		return batchenergyoptimization.Trial{}, fmt.Errorf("%w: campaign Run requires a positive logical batch and attempt", errInvalidCampaignTrial)
	}
	var mode batchenergyoptimization.WorkloadMode
	switch run.Spec.Measurement.WorkloadMode {
	case activeWorkloadMode:
		mode = batchenergyoptimization.WorkloadModeActive
	case idleWorkloadMode:
		mode = batchenergyoptimization.WorkloadModeIdle
	default:
		return batchenergyoptimization.Trial{}, errors.New("campaign Run has an unsupported workload mode")
	}
	measurement := batchenergyoptimization.Measurement{
		WorkloadMode: mode, Batch: int(identity.Batch),
	}
	attempt := int(identity.Attempt)
	// Idle execution batch is checked against the resolved backend at the
	// verified curve-state boundary, not inferred from this object's path name.
	if mode == batchenergyoptimization.WorkloadModeActive && run.Spec.Measurement.Batch != identity.Batch {
		return batchenergyoptimization.Trial{}, fmt.Errorf("%w: campaign Run execution batch disagrees with its logical trial", errInvalidCampaignTrial)
	}
	trial := batchenergyoptimization.Trial{
		Measurement: measurement,
		Attempt:     attempt,
		Verdict:     batchenergyoptimization.VerdictPending,
	}
	if run.Status.Evidence != nil {
		trial.Verdict = batchenergyoptimization.VerdictCompleted
		return trial, nil
	}
	if run.Status.HasTerminalFailure() {
		trial.Verdict = batchenergyoptimization.VerdictFailed
		trial.FailureClass = projectFailureClass(run)
		if trial.FailureClass == batchenergyoptimization.FailureClassExpectedStop {
			trial.Boundary = projectDiscoveryBoundary(run, measurement, attempt)
			if trial.Boundary == nil {
				trial.FailureClass = batchenergyoptimization.FailureClassUnknownFailure
			}
		}
	}
	return trial, nil
}

func projectFailureClass(run *edgev1alpha1.ProfilingRun) batchenergyoptimization.FailureClass {
	for _, condition := range run.Status.Conditions {
		if condition.Status != metav1.ConditionFalse ||
			(condition.Type != edgev1alpha1.ProfilingRunConditionExecutionComplete &&
				condition.Type != edgev1alpha1.ProfilingRunConditionEvidenceVerified) {
			continue
		}
		switch condition.Reason {
		case "EvidenceInvalid", "InvalidRunSpec", "ExecutionPolicyInvalid", "ActuationPolicyInvalid", "RequiredStateNotConfirmed":
			return batchenergyoptimization.FailureClassFatalFailure
		}
	}
	if termination := run.Status.RuntimeTermination; termination != nil {
		switch class := batchenergyoptimization.FailureClass(termination.Class); class {
		case batchenergyoptimization.FailureClassExpectedStop,
			batchenergyoptimization.FailureClassRetryableFailure,
			batchenergyoptimization.FailureClassFatalFailure,
			batchenergyoptimization.FailureClassUnknownFailure:
			return class
		}
	}
	return batchenergyoptimization.FailureClassUnknownFailure
}

func projectDiscoveryBoundary(run *edgev1alpha1.ProfilingRun, measurement batchenergyoptimization.Measurement, attempt int) *batchenergyoptimization.DiscoveryStop {
	termination := run.Status.RuntimeTermination
	if termination == nil || termination.Class != profilerevidence.FailureClassExpectedStop || termination.Container != "runtime" {
		return nil
	}
	var reason string
	switch {
	case termination.Stage == profilerevidence.StageExecution && (termination.Reason == "OOMKilled" || termination.FailureReason == profilerevidence.FailureMemoryAllocation):
		reason = batchenergyoptimization.DiscoveryStopOOM
	case termination.Stage == profilerevidence.StageExecution && (termination.FailureReason == profilerevidence.FailureMeasurementBudget || termination.JobReason == "DeadlineExceeded"):
		reason = batchenergyoptimization.DiscoveryStopMeasurementBudgetExceeded
	default:
		return nil
	}
	return &batchenergyoptimization.DiscoveryStop{
		Reason: reason, FailedMeasurement: &measurement, Attempt: attempt,
		Run:        &batchenergyoptimization.RunReference{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)},
		Diagnostic: runtimeTerminationDiagnostic(termination),
	}
}

func runtimeTerminationDiagnostic(termination *edgev1alpha1.ProfilingRunRuntimeTermination) string {
	return fmt.Sprintf("class=%s container=%s stage=%s reason=%s failureReason=%s exitCode=%d podReason=%s podMessage=%s jobReason=%s jobMessage=%s",
		termination.Class, termination.Container, termination.Stage, termination.Reason, termination.FailureReason,
		termination.ExitCode, termination.PodReason, termination.PodMessage, termination.JobReason, termination.JobMessage)
}
