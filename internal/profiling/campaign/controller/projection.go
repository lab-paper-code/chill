package controller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectSnapshot verifies ownership and projects each child. Labels select
// candidate children; projected measurement and attempt values come from the
// immutable Run spec and name. transition is the Node's projected power mode.
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
	mode := batchenergyoptimization.WorkloadModeActive
	if run.Spec.Measurement.WorkloadMode == idleWorkloadMode {
		mode = batchenergyoptimization.WorkloadModeIdle
	}
	measurement := batchenergyoptimization.Measurement{
		WorkloadMode: mode, Batch: int(run.Spec.Measurement.Batch),
	}
	if mode == batchenergyoptimization.WorkloadModeIdle {
		// Idle trial identity retains the planned batch; its measurement batch
		// is independently fixed at 1 except for RKNN.
		encoded := strings.TrimPrefix(run.Name, parentName+"-idle-b")
		batch, _, found := strings.Cut(encoded, "-a")
		value, err := strconv.Atoi(batch)
		if !found || err != nil || value < 1 {
			return batchenergyoptimization.Trial{}, errors.New("idle Run name has no usable batch")
		}
		measurement.Batch = value
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
	if len(run.Status.TerminalFailureReasons()) > 0 {
		trial.Verdict = batchenergyoptimization.VerdictFailed
	}
	return trial, nil
}
