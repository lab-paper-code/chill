package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
)

func projectRunActuationSnapshot(
	handoff *edgev1alpha1.NodeActuation,
	runSnapshot profiler.Snapshot,
) (profiler.RunActuationSnapshot, error) {
	if handoff == nil {
		return profiler.RunActuationSnapshot{}, errors.New("NodeActuation is required")
	}
	snapshot := profiler.RunActuationSnapshot{
		JobRecorded: runSnapshot.JobReference != "",
	}
	switch handoff.Status.Phase {
	case "", edgev1alpha1.NodeActuationPhasePending,
		edgev1alpha1.NodeActuationPhaseAcquiringLease,
		edgev1alpha1.NodeActuationPhaseCheckingSafety,
		edgev1alpha1.NodeActuationPhaseObservingPrevious,
		edgev1alpha1.NodeActuationPhaseApplying,
		edgev1alpha1.NodeActuationPhaseVerifying,
		edgev1alpha1.NodeActuationPhaseSettling:
		snapshot.State = profiler.RunActuationPending
	case edgev1alpha1.NodeActuationPhaseReady:
		snapshot.State = profiler.RunActuationReady
	case edgev1alpha1.NodeActuationPhaseFailed:
		if handoff.Status.TerminalFailure == nil {
			return profiler.RunActuationSnapshot{}, errors.New("failed NodeActuation omitted terminal failure")
		}
		snapshot.State = profiler.RunActuationFailed
	default:
		return profiler.RunActuationSnapshot{}, fmt.Errorf(
			"unsupported NodeActuation phase %q", handoff.Status.Phase,
		)
	}
	if err := snapshot.Validate(); err != nil {
		return profiler.RunActuationSnapshot{}, fmt.Errorf("validate Run actuation snapshot: %w", err)
	}
	return snapshot, nil
}
