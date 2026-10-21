package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const admissionWait = 5 * time.Second

type measurementAdvancer interface {
	advance(context.Context, *edgev1alpha1.PowerModeTransitionCost) (measurementProgress, error)
}

type measurementResultRecorder interface {
	write(
		context.Context,
		*edgev1alpha1.PowerModeTransitionCost,
		measurementProgress,
	) (storedMeasurementResult, error)
}

// powerModeTransitionCostReconciler encloses one immutable preparation,
// alternating attempt sequence, terminal result, and compact status.
type powerModeTransitionCostReconciler struct {
	client   client.Client
	sequence measurementAdvancer
	results  measurementResultRecorder
}

func newPowerModeTransitionCostReconciler(
	kubeClient client.Client,
	sequence measurementAdvancer,
	results measurementResultRecorder,
) (*powerModeTransitionCostReconciler, error) {
	if kubeClient == nil || sequence == nil || results == nil {
		return nil, errors.New("Kubernetes client, transition sequence, and result recorder are required")
	}
	return &powerModeTransitionCostReconciler{
		client: kubeClient, sequence: sequence, results: results,
	}, nil
}

func (r *powerModeTransitionCostReconciler) Reconcile(
	ctx context.Context,
	request ctrl.Request,
) (ctrl.Result, error) {
	measurement := &edgev1alpha1.PowerModeTransitionCost{}
	if err := r.client.Get(ctx, request.NamespacedName, measurement); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !measurement.DeletionTimestamp.IsZero() || completeTerminalMeasurementStatus(measurement) {
		return ctrl.Result{}, nil
	}
	// A measurement that has not reported Measuring waits for ChillServer's
	// admission before its preparation record, Jobs, or NodeActuations.
	if measurement.Status.Phase != edgev1alpha1.PowerModeTransitionCostPhaseMeasuring {
		granted, _, refused := profilingrun.Admission(measurement.Annotations)
		if refused != "" {
			// The status has no reason field; the reason stays in the admission annotation.
			status := projectMeasurementStatus(
				measurement, edgev1alpha1.PowerModeTransitionCostPhaseFailed,
				make([]measurementResultDirection, 2), nil,
			)
			return ctrl.Result{}, r.patchStatus(ctx, measurement, status)
		}
		if !granted {
			return ctrl.Result{RequeueAfter: admissionWait}, nil
		}
	}

	progress, err := r.sequence.advance(ctx, measurement)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("advance power-mode transition-cost measurement: %w", err)
	}
	switch progress.stage {
	case measurementStageAttemptActive, measurementStageAttemptFinalized:
		status := projectMeasurementStatus(
			measurement,
			edgev1alpha1.PowerModeTransitionCostPhaseMeasuring,
			measurementResultDirections(progress.attempts),
			nil,
		)
		if err := r.patchStatus(ctx, measurement, status); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: progress.stage == measurementStageAttemptFinalized}, nil
	case measurementStageComplete, measurementStageInsufficient, measurementStageFailed:
		stored, err := r.results.write(ctx, measurement, progress)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("write power-mode transition-cost result: %w", err)
		}
		phase := edgev1alpha1.PowerModeTransitionCostPhaseFailed
		if stored.record.Outcome == measurementStageComplete {
			phase = edgev1alpha1.PowerModeTransitionCostPhaseComplete
		}
		status := projectMeasurementStatus(
			measurement, phase, stored.record.Directions, &stored.reference,
		)
		if err := r.patchStatus(ctx, measurement, status); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported transition measurement stage %q", progress.stage)
	}
}

func projectMeasurementStatus(
	measurement *edgev1alpha1.PowerModeTransitionCost,
	phase edgev1alpha1.PowerModeTransitionCostPhase,
	directions []measurementResultDirection,
	result *edgev1alpha1.ProfilingRunEvidenceReference,
) edgev1alpha1.PowerModeTransitionCostStatus {
	progress := make([]edgev1alpha1.PowerModeTransitionCostDirectionProgress, 2)
	for index := range progress {
		progress[index] = edgev1alpha1.PowerModeTransitionCostDirectionProgress{
			SourceMode:    measurement.Spec.Modes[index].PowerMode,
			TargetMode:    measurement.Spec.Modes[1-index].PowerMode,
			AttemptCount:  int32(len(directions[index].FinalEvidence)),
			AcceptedCount: directions[index].AcceptedCount,
		}
	}
	return edgev1alpha1.PowerModeTransitionCostStatus{
		ObservedGeneration: measurement.Generation,
		Phase:              phase,
		Progress:           progress,
		Result:             result,
	}
}

func completeTerminalMeasurementStatus(measurement *edgev1alpha1.PowerModeTransitionCost) bool {
	status := measurement.Status
	if status.ObservedGeneration != measurement.Generation || status.Result == nil || len(status.Progress) != 2 ||
		(status.Phase != edgev1alpha1.PowerModeTransitionCostPhaseComplete &&
			status.Phase != edgev1alpha1.PowerModeTransitionCostPhaseFailed) {
		return false
	}
	return status.Progress[0].SourceMode == measurement.Spec.Modes[0].PowerMode &&
		status.Progress[0].TargetMode == measurement.Spec.Modes[1].PowerMode &&
		status.Progress[1].SourceMode == measurement.Spec.Modes[1].PowerMode &&
		status.Progress[1].TargetMode == measurement.Spec.Modes[0].PowerMode
}

func (r *powerModeTransitionCostReconciler) patchStatus(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	status edgev1alpha1.PowerModeTransitionCostStatus,
) error {
	if sameMeasurementStatus(measurement.Status, status) {
		return nil
	}
	base := measurement.DeepCopy()
	desired := measurement.DeepCopy()
	desired.Status = status
	if err := r.client.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch PowerModeTransitionCost status: %w", err)
	}
	return nil
}

func sameMeasurementStatus(
	left edgev1alpha1.PowerModeTransitionCostStatus,
	right edgev1alpha1.PowerModeTransitionCostStatus,
) bool {
	if left.ObservedGeneration != right.ObservedGeneration || left.Phase != right.Phase ||
		len(left.Progress) != 2 || len(right.Progress) != 2 {
		return false
	}
	for index := 0; index < 2; index++ {
		if left.Progress[index].SourceMode != right.Progress[index].SourceMode ||
			left.Progress[index].TargetMode != right.Progress[index].TargetMode ||
			left.Progress[index].AttemptCount != right.Progress[index].AttemptCount ||
			left.Progress[index].AcceptedCount != right.Progress[index].AcceptedCount {
			return false
		}
	}
	if left.Result == nil || right.Result == nil {
		return left.Result == nil && right.Result == nil
	}
	return left.Result.Store == right.Result.Store && left.Result.Key == right.Result.Key &&
		left.Result.SchemaVersion == right.Result.SchemaVersion && left.Result.Size == right.Result.Size
}
