package controller

import (
	"errors"
	"fmt"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

func projectRunActuationSnapshot(
	handoff *edgev1alpha1.NodeActuation,
	runSnapshot profiler.Snapshot,
	job *batchv1.Job,
) (profiler.RunActuationSnapshot, error) {
	if handoff == nil {
		return profiler.RunActuationSnapshot{}, errors.New("NodeActuation is required")
	}
	snapshot := profiler.RunActuationSnapshot{
		JobRecorded: runSnapshot.JobReference != "",
		Job:         runSnapshot.Job,
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
	case edgev1alpha1.NodeActuationPhaseRestoring,
		edgev1alpha1.NodeActuationPhaseVerifyingRestore:
		snapshot.State = profiler.RunActuationRestoring
	case edgev1alpha1.NodeActuationPhaseRestored:
		snapshot.State = profiler.RunActuationRestored
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
	if handoff.Spec.DesiredStage == edgev1alpha1.NodeActuationDesiredStageRestore {
		snapshot.RestoreRequested = true
		snapshot.RestoreRequestedAt = handoff.Spec.StageRequestedAt.Time
	} else if handoff.Spec.DesiredStage != edgev1alpha1.NodeActuationDesiredStageApply {
		return profiler.RunActuationSnapshot{}, fmt.Errorf(
			"unsupported NodeActuation desired stage %q", handoff.Spec.DesiredStage,
		)
	}
	if runSnapshot.Job == profiler.JobSucceeded {
		finishedAt, err := successfulJobFinishedAt(job)
		if err != nil {
			return profiler.RunActuationSnapshot{}, err
		}
		snapshot.JobFinishedAt = finishedAt
	}
	if err := snapshot.Validate(); err != nil {
		return profiler.RunActuationSnapshot{}, fmt.Errorf("validate Run actuation snapshot: %w", err)
	}
	return snapshot, nil
}

func successfulJobFinishedAt(job *batchv1.Job) (time.Time, error) {
	if job == nil {
		return time.Time{}, errors.New("successful Job object is required")
	}
	if job.Status.CompletionTime != nil && !job.Status.CompletionTime.IsZero() {
		return job.Status.CompletionTime.Time, nil
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue &&
			!condition.LastTransitionTime.IsZero() {
			return condition.LastTransitionTime.Time, nil
		}
	}
	return time.Time{}, errors.New("successful Job omitted a terminal timestamp")
}
