package controller

import (
	"context"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// reconcileActuatedRun owns only the Profiler side of the durable handoff:
// wait for target readiness, gate Job creation, request restoration after a
// terminal Job, and release the existing evidence lifecycle after restoration.
func (r *RunReconciler) reconcileActuatedRun(
	ctx context.Context,
	request ctrl.Request,
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
	job *batchv1.Job,
) (bool, error) {
	handoff, _, err := r.handoffs.Ensure(ctx, run, frozen)
	if err != nil {
		return true, fmt.Errorf(
			"materialize ProfilingRun %s actuation handoff: %w", request.NamespacedName, err,
		)
	}
	if err := r.publishActuationReference(ctx, run, handoff); err != nil {
		return true, fmt.Errorf(
			"publish ProfilingRun %s actuation reference: %w", request.NamespacedName, err,
		)
	}
	runSnapshot, err := ProjectSnapshot(run, &frozen.Prepared.Intent, job)
	if err != nil {
		return true, fmt.Errorf("project ProfilingRun %s lifecycle: %w", request.NamespacedName, err)
	}
	actuationSnapshot, err := projectRunActuationSnapshot(handoff, runSnapshot, job)
	if err != nil {
		return true, fmt.Errorf(
			"project ProfilingRun %s actuation lifecycle: %w", request.NamespacedName, err,
		)
	}
	decision, err := profiler.DecideRunActuation(actuationSnapshot)
	if err != nil {
		return true, fmt.Errorf(
			"decide ProfilingRun %s actuation lifecycle: %w", request.NamespacedName, err,
		)
	}
	switch decision.Action {
	case profiler.RunActuationWaitForReady:
		return true, r.publishExecutionOutcome(
			ctx, run, metav1.ConditionUnknown, "NodeActuatorPending",
			"durable Node actuation handoff is recorded; measurement waits for verified and settled target state",
		)
	case profiler.RunActuationCreateJob:
		return true, r.ensureMeasurementJob(ctx, request, run, frozen)
	case profiler.RunActuationHoldTarget:
		return true, r.publishExecutionOutcome(
			ctx, run, metav1.ConditionUnknown, "JobRunning",
			"measurement Job has not completed; verified target state remains held",
		)
	case profiler.RunActuationRequestRestore:
		return true, r.requestRunRestoration(ctx, request, run, frozen, handoff)
	case profiler.RunActuationWaitForRestore:
		return true, r.publishExecutionOutcome(
			ctx, run, metav1.ConditionUnknown, "NodeActuatorRestoring",
			"measurement outcome is withheld until previous Node state restoration is independently verified",
		)
	case profiler.RunActuationReportFailure:
		return true, r.publishActuationFailure(ctx, run, handoff, decision.Failure)
	case profiler.RunActuationContinue:
		return false, nil
	default:
		return true, fmt.Errorf("unexpected ProfilingRun actuation action %q", decision.Action)
	}
}

func (r *RunReconciler) requestRunRestoration(
	ctx context.Context,
	request ctrl.Request,
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
	handoff *edgev1alpha1.NodeActuation,
) error {
	changed, err := r.handoffs.AdvanceToRestore(ctx, run, frozen, handoff)
	if err != nil {
		return fmt.Errorf("request ProfilingRun %s restoration: %w", request.NamespacedName, err)
	}
	message := "measurement execution ended; previous Node state restoration was requested"
	if !changed {
		message = "measurement execution ended after the immutable deadline; fail-safe Node restoration is awaited"
	}
	return r.publishExecutionOutcome(ctx, run, metav1.ConditionUnknown, "NodeActuatorRestoring", message)
}

func (r *RunReconciler) publishActuationFailure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	handoff *edgev1alpha1.NodeActuation,
	failure profiler.RunActuationFailure,
) error {
	reason := "NodeActuationFailed"
	message := "Node actuation did not preserve one valid measurement execution"
	if handoff.Status.TerminalFailure != nil {
		terminal := handoff.Status.TerminalFailure
		if terminal.Stage == edgev1alpha1.NodeActuationFailureStageRestore {
			reason = "NodeRestoreFailed"
		}
		message = fmt.Sprintf(
			"Node actuation failed at %s: %s: %s", terminal.Stage, terminal.Reason, terminal.Message,
		)
	} else {
		switch failure {
		case profiler.RunActuationEndedBeforeExecution:
			reason = "NodeActuationEndedBeforeExecution"
			message = "previous Node state was restored before a measurement Job was created"
		case profiler.RunActuationInterruptedExecution:
			reason = "NodeActuationInterruptedExecution"
			message = "previous Node state was restored while the measurement Job was still active"
		case profiler.RunActuationRestoreNotRequested:
			reason = "NodeActuationOrderingInvalid"
			message = "previous Node state was restored without a Profiler-owned restore request"
		case profiler.RunActuationRestoreTooEarly:
			reason = "NodeActuationOrderingInvalid"
			message = "the restore request preceded successful measurement Job completion"
		}
	}
	return r.publishExecutionOutcome(ctx, run, metav1.ConditionFalse, reason, message)
}
