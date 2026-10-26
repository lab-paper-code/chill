package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// attemptJobCleanup never supplies TTL at creation: raw receipts must survive
// until immutable final evidence (or the terminal measurement failure) is retained.
type attemptJobCleanup struct {
	client                    client.Client
	finalizer                 *AttemptFinalizer
	failedJobRetentionSeconds int32
}

func (c *attemptJobCleanup) cleanup(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	evidenceStore string,
) (bool, error) {
	jobs := &batchv1.JobList{}
	if err := c.client.List(ctx, jobs, client.InNamespace(measurement.Namespace)); err != nil {
		return true, fmt.Errorf("list transition attempt Jobs for cleanup: %w", err)
	}
	pending := false
	var failures []error
	for index := range jobs.Items {
		job := &jobs.Items[index]
		if !metav1.IsControlledBy(job, measurement) {
			continue
		}
		if !job.DeletionTimestamp.IsZero() {
			continue
		}
		if !jobComplete(job) && !jobFailed(job) {
			pending = true
			continue
		}
		if err := c.cleanupJob(ctx, measurement, evidenceStore, job); err != nil {
			failures = append(failures, fmt.Errorf("clean transition attempt Job %q: %w", job.Name, err))
		}
	}
	return pending || len(failures) != 0, errors.Join(failures...)
}

func (c *attemptJobCleanup) cleanupJob(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	evidenceStore string,
	job *batchv1.Job,
) error {
	direction, err := strconv.Atoi(job.Annotations[transitionDirectionIndexAnnotation])
	if err != nil {
		return errors.New("retained transition attempt Job has no valid direction")
	}
	attemptText, found := strings.CutPrefix(job.Name,
		"transition-"+string(measurement.UID)+"-d"+strconv.Itoa(direction)+"-a")
	if !found {
		return errors.New("retained transition attempt Job has no attempt number")
	}
	attempt, err := strconv.ParseInt(attemptText, 10, 32)
	if err != nil {
		return fmt.Errorf("parse retained transition attempt number: %w", err)
	}
	selection, err := selectAttempt(measurement, evidenceStore, direction, int32(attempt))
	if err != nil {
		return err
	}
	if job.Name != selection.name || job.UID == "" {
		return errors.New("retained transition attempt Job has unexpected name or missing UID")
	}
	finalized, found, err := c.finalizer.load(ctx, selection.finalLocation)
	if err != nil {
		return err
	}
	if !found && !(completeTerminalMeasurementStatus(measurement) &&
		measurement.Status.Phase == edgev1alpha1.PowerModeTransitionCostPhaseFailed) {
		// The active attempt still needs its Pod receipt; no cleanup is safe yet.
		return nil
	}
	if found && jobComplete(job) && !jobFailed(job) && finalized.Observation.Verdict() == transitioncost.VerdictAccepted {
		return client.IgnoreNotFound(c.client.Delete(ctx, job,
			client.PropagationPolicy(metav1.DeletePropagationBackground),
			client.Preconditions{UID: &job.UID},
		))
	}
	if job.Spec.TTLSecondsAfterFinished != nil && *job.Spec.TTLSecondsAfterFinished == c.failedJobRetentionSeconds {
		return nil
	}
	base := job.DeepCopy()
	job.Spec.TTLSecondsAfterFinished = &c.failedJobRetentionSeconds
	return client.IgnoreNotFound(c.client.Patch(ctx, job,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}),
	))
}
