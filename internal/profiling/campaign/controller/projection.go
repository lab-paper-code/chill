package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
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
	if err := snapshot.Validate(); err != nil {
		return batchenergyoptimization.Snapshot{}, fmt.Errorf("validate projected snapshot: %w", err)
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
	if run.Name != batchenergyoptimization.RunName(parentName, measurement, attempt) {
		return batchenergyoptimization.Trial{}, fmt.Errorf("%w: Run name does not match its immutable campaign trial", errInvalidCampaignTrial)
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
	}
	return trial, nil
}
