package controller

import (
	"errors"
	"fmt"
	"reflect"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectSnapshot verifies ownership and projects each child. Labels select
// candidate children; projected measurement and attempt values come from the
// immutable Run spec and name.
func ProjectSnapshot(
	parent *edgev1alpha1.BatchEnergyOptimization,
	runs []edgev1alpha1.ProfilingRun,
	transitions []edgev1alpha1.NodeActuation,
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
		TransitionRequired: transitionRequired,
		RetryLimit:         int(parent.Spec.RetryLimit),
		PlannedBatchSizes:  append([]int(nil), plannedBatchSizes...),
	}
	if len(transitions) > 1 {
		return batchenergyoptimization.Snapshot{}, errors.New("a BatchEnergyOptimization cannot own more than one transition")
	}
	if len(transitions) == 1 {
		transition, err := projectOwnedTransition(parent, &transitions[0])
		if err != nil {
			return batchenergyoptimization.Snapshot{}, fmt.Errorf("project NodeActuation %q: %w", transitions[0].Name, err)
		}
		snapshot.Transition = &transition
	}
	for index := range runs {
		run := &runs[index]
		trial, err := projectOwnedTrial(parent, run)
		if err != nil {
			return batchenergyoptimization.Snapshot{}, fmt.Errorf("project ProfilingRun %q: %w", run.Name, err)
		}
		snapshot.Trials = append(snapshot.Trials, trial)
	}
	if err := snapshot.Validate(); err != nil {
		return batchenergyoptimization.Snapshot{}, fmt.Errorf("validate projected snapshot: %w", err)
	}
	return snapshot, nil
}

func projectOwnedTransition(
	parent *edgev1alpha1.BatchEnergyOptimization,
	transition *edgev1alpha1.NodeActuation,
) (batchenergyoptimization.Transition, error) {
	if !controlledByParent(parent, transition) {
		return batchenergyoptimization.Transition{}, errors.New("transition is not controller-owned by this BatchEnergyOptimization")
	}
	if transition.Name != TransitionName(parent.Name) || transition.Namespace != parent.Namespace ||
		transition.Spec.Node.Name != parent.Spec.State.TargetNode ||
		transition.Spec.DeviceClass != parent.Spec.State.Candidate.DeviceClass ||
		transition.Spec.PowerMode != parent.Spec.State.Candidate.PowerMode {
		return batchenergyoptimization.Transition{}, errors.New("transition does not match the campaign state")
	}
	switch transition.Status.Phase {
	case "", edgev1alpha1.NodeActuationPhasePending,
		edgev1alpha1.NodeActuationPhaseAcquiringLease,
		edgev1alpha1.NodeActuationPhaseObservingPrevious,
		edgev1alpha1.NodeActuationPhaseApplying,
		edgev1alpha1.NodeActuationPhaseVerifying,
		edgev1alpha1.NodeActuationPhaseSettling:
		return batchenergyoptimization.Transition{Outcome: batchenergyoptimization.TransitionInFlight}, nil
	case edgev1alpha1.NodeActuationPhaseFailed:
		if transition.Status.TerminalFailure == nil {
			return batchenergyoptimization.Transition{}, errors.New("failed transition omitted its terminal failure")
		}
		return batchenergyoptimization.Transition{Outcome: batchenergyoptimization.TransitionFailed}, nil
	case edgev1alpha1.NodeActuationPhaseReady:
		observation := transition.Status.TargetObservation
		if transition.Generation < 1 || transition.Status.ObservedGeneration != transition.Generation ||
			transition.Status.TerminalFailure != nil || observation == nil ||
			transition.Status.ReadyAt == nil || transition.Status.ReadyAt.IsZero() ||
			observation.PowerMode != transition.Spec.PowerMode ||
			!reflect.DeepEqual(observation.Actuator, transition.Spec.Actuator) {
			return batchenergyoptimization.Transition{}, errors.New("ready transition does not prove its declared target")
		}
		state := batchenergyoptimization.State{
			TargetNode:  transition.Spec.Node.Name,
			DeviceClass: transition.Spec.DeviceClass,
			PowerMode:   transition.Spec.PowerMode,
		}
		return batchenergyoptimization.Transition{
			Outcome: batchenergyoptimization.TransitionEstablished, EstablishedState: &state,
		}, nil
	default:
		return batchenergyoptimization.Transition{}, fmt.Errorf("unsupported transition phase %q", transition.Status.Phase)
	}
}

func projectOwnedTrial(
	parent *edgev1alpha1.BatchEnergyOptimization,
	run *edgev1alpha1.ProfilingRun,
) (batchenergyoptimization.Trial, error) {
	if !controlledByParent(parent, run) {
		return batchenergyoptimization.Trial{}, errors.New("Run is not controller-owned by this BatchEnergyOptimization")
	}
	return projectTrial(parent.Name, run)
}

// controlledByParent reports whether the parent's exact UID is the object's
// controller owner.
func controlledByParent(parent *edgev1alpha1.BatchEnergyOptimization, object metav1.Object) bool {
	owner := metav1.GetControllerOf(object)
	return owner != nil && owner.Kind == "BatchEnergyOptimization" &&
		owner.APIVersion == edgev1alpha1.GroupVersion.String() &&
		parent.UID != "" && owner.UID == parent.UID
}

func projectTrial(parentName string, run *edgev1alpha1.ProfilingRun) (batchenergyoptimization.Trial, error) {
	mode := batchenergyoptimization.WorkloadModeActive
	if run.Spec.Measurement.WorkloadMode == idleWorkloadMode {
		mode = batchenergyoptimization.WorkloadModeIdle
	}
	measurement := batchenergyoptimization.Measurement{
		WorkloadMode: mode, Batch: int(run.Spec.Measurement.Batch),
	}
	attempt, err := attemptFromRunName(parentName, measurement, run.Name)
	if err != nil {
		return batchenergyoptimization.Trial{}, err
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
	if len(terminalFailureReasons(run.Status.Conditions)) > 0 {
		trial.Verdict = batchenergyoptimization.VerdictFailed
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
	return conditionType == "ExecutionComplete" ||
		conditionType == edgev1alpha1.ProfilingRunConditionEvidenceVerified
}

func isTerminalReason(reason string) bool {
	switch reason {
	case "InvalidRunSpec",
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
