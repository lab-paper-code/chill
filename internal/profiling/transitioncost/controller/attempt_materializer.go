package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	AttemptStageObserverStarting  AttemptStage = "ObserverStarting"
	AttemptStageActuationRunning  AttemptStage = "ActuationRunning"
	AttemptStageFinalizationReady AttemptStage = "FinalizationReady"
	AttemptStageTerminal          AttemptStage = "Terminal"

	AttemptReasonSourceStateUnknown  = "SourceStateUnknown"
	AttemptReasonSourceStateMismatch = "SourceStateMismatch"
	AttemptReasonObserverFailed      = "ObserverFailed"
	AttemptReasonObserverEndedEarly  = "ObserverEndedBeforeActuation"
)

type AttemptStage string

// AttemptStoreBindingResolver resolves delivery material only when an attempt
// Job does not already exist. The Job is the retained delivery contract after
// creation.
type AttemptStoreBindingResolver interface {
	Resolve(context.Context) (storekube.Binding, error)
}

// AttemptProgress is the narrow handoff from materialization to the following
// evidence finalizer. It does not decide another direction or attempt.
type AttemptProgress struct {
	Stage         AttemptStage
	Reason        string
	Job           *batchv1.Job
	NodeActuation *edgev1alpha1.NodeActuation
}

// AttemptMaterializer advances exactly one caller-selected directed attempt.
// It is deliberately not registered as a controller until evidence
// finalization can consume FinalizationReady in the same lifecycle.
type AttemptMaterializer struct {
	client       client.Client
	scheme       *runtime.Scheme
	preparations *PreparationRecords
	stores       AttemptStoreBindingResolver
	options      AttemptJobOptions
	now          func() time.Time
}

func NewAttemptMaterializer(
	kubeClient client.Client,
	scheme *runtime.Scheme,
	preparations *PreparationRecords,
	stores AttemptStoreBindingResolver,
	options AttemptJobOptions,
) (*AttemptMaterializer, error) {
	if kubeClient == nil || scheme == nil || preparations == nil || stores == nil {
		return nil, errors.New("Kubernetes client, scheme, preparation records, and Evidence Store resolver are required")
	}
	options.Resources = *options.Resources.DeepCopy()
	options.Tolerations = cloneTolerations(options.Tolerations)
	return &AttemptMaterializer{
		client: kubeClient, scheme: scheme, preparations: preparations,
		stores: stores, options: options, now: time.Now,
	}, nil
}

// Advance creates at most one Job and its same-name NodeActuation. Repeated
// calls reconstruct progress from the immutable preparation and native owned
// objects; they never select a later attempt.
func (m *AttemptMaterializer) Advance(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	directionIndex int,
	attemptNumber int32,
) (AttemptProgress, error) {
	selection, err := selectAttempt(measurement, directionIndex, attemptNumber)
	if err != nil {
		return AttemptProgress{}, err
	}
	prepared, _, err := m.preparations.Resolve(ctx, measurement)
	if err != nil {
		return AttemptProgress{}, fmt.Errorf("resolve immutable transition preparation: %w", err)
	}

	job, err := m.loadJob(ctx, measurement, selection)
	if err != nil {
		return AttemptProgress{}, err
	}
	actuation, err := m.loadActuation(ctx, measurement, selection)
	if err != nil {
		return AttemptProgress{}, err
	}
	if actuation != nil {
		if job == nil {
			return AttemptProgress{}, errors.New("transition attempt NodeActuation exists without its observer Job")
		}
		return progressForRetainedAttempt(job, actuation), nil
	}
	if job == nil {
		confirmed, reason, err := m.confirmSource(ctx, prepared, directionIndex)
		if err != nil {
			return AttemptProgress{}, err
		}
		if !confirmed {
			return AttemptProgress{Stage: AttemptStageTerminal, Reason: reason}, nil
		}
		binding, err := m.stores.Resolve(ctx)
		if err != nil {
			return AttemptProgress{}, fmt.Errorf("resolve transition attempt Evidence Store binding: %w", err)
		}
		job, err = BuildAttemptJob(AttemptJobRequest{
			Measurement: measurement, Prepared: prepared, DirectionIndex: directionIndex,
			NodeActuationName: selection.name, RawLocation: selection.rawLocation,
			StoreBinding: binding,
		}, m.options, m.scheme)
		if err != nil {
			return AttemptProgress{}, err
		}
		if err := m.client.Create(ctx, job); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return AttemptProgress{}, fmt.Errorf("create transition attempt Job %q: %w", selection.name, err)
			}
			job, err = m.loadJob(ctx, measurement, selection)
			if err != nil {
				return AttemptProgress{}, err
			}
		}
		return AttemptProgress{Stage: AttemptStageObserverStarting, Job: job}, nil
	}

	if jobFailed(job) {
		return AttemptProgress{Stage: AttemptStageTerminal, Reason: AttemptReasonObserverFailed, Job: job}, nil
	}
	if jobComplete(job) {
		return AttemptProgress{Stage: AttemptStageTerminal, Reason: AttemptReasonObserverEndedEarly, Job: job}, nil
	}
	if job.Status.Ready == nil || *job.Status.Ready != 1 {
		return AttemptProgress{Stage: AttemptStageObserverStarting, Job: job}, nil
	}

	confirmed, reason, err := m.confirmSource(ctx, prepared, directionIndex)
	if err != nil {
		return AttemptProgress{}, err
	}
	if !confirmed {
		return AttemptProgress{Stage: AttemptStageTerminal, Reason: reason, Job: job}, nil
	}
	actuation, err = buildAttemptActuation(measurement, prepared, selection, m.now(), m.scheme)
	if err != nil {
		return AttemptProgress{}, err
	}
	if err := m.client.Create(ctx, actuation); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return AttemptProgress{}, fmt.Errorf("create transition attempt NodeActuation %q: %w", selection.name, err)
		}
		actuation, err = m.loadActuation(ctx, measurement, selection)
		if err != nil {
			return AttemptProgress{}, err
		}
	}
	return AttemptProgress{
		Stage: AttemptStageActuationRunning, Job: job, NodeActuation: actuation,
	}, nil
}

type attemptSelection struct {
	directionIndex int
	name           string
	rawLocation    evidencestore.ObjectLocation
	finalLocation  evidencestore.ObjectLocation
}

func selectAttempt(
	measurement *edgev1alpha1.PowerModeTransitionCost,
	directionIndex int,
	attemptNumber int32,
) (attemptSelection, error) {
	if measurement == nil || measurement.UID == "" {
		return attemptSelection{}, errors.New("PowerModeTransitionCost with a UID is required")
	}
	if directionIndex < 0 || directionIndex > 1 {
		return attemptSelection{}, errors.New("transition direction index must be zero or one")
	}
	if attemptNumber < 1 || attemptNumber > measurement.Spec.AttemptLimit {
		return attemptSelection{}, errors.New("transition attempt number is outside the frozen attempt limit")
	}
	suffix := "d" + strconv.Itoa(directionIndex) + "-a" + strconv.FormatInt(int64(attemptNumber), 10)
	name := "transition-" + string(measurement.UID) + "-" + suffix
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return attemptSelection{}, fmt.Errorf("deterministic transition attempt name is invalid: %v", errs)
	}
	prefix := "power-mode-transition-costs/" + string(measurement.UID) + "/" + suffix + "/"
	return attemptSelection{
		directionIndex: directionIndex, name: name,
		rawLocation:   evidencestore.ObjectLocation{Store: measurement.Spec.EvidenceStore, Key: prefix + "raw.json"},
		finalLocation: evidencestore.ObjectLocation{Store: measurement.Spec.EvidenceStore, Key: prefix + "final.json"},
	}, nil
}

func (m *AttemptMaterializer) loadJob(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	selection attemptSelection,
) (*batchv1.Job, error) {
	job := &batchv1.Job{}
	key := types.NamespacedName{Namespace: measurement.Namespace, Name: selection.name}
	if err := m.client.Get(ctx, key, job); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get transition attempt Job %s: %w", key, err)
	}
	if !metav1.IsControlledBy(job, measurement) ||
		job.Annotations[transitionDirectionIndexAnnotation] != strconv.Itoa(selection.directionIndex) {
		return nil, errors.New("retained transition attempt Job has unexpected ownership or direction")
	}
	return job, nil
}

func (m *AttemptMaterializer) loadActuation(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	selection attemptSelection,
) (*edgev1alpha1.NodeActuation, error) {
	actuation := &edgev1alpha1.NodeActuation{}
	key := types.NamespacedName{Namespace: measurement.Namespace, Name: selection.name}
	if err := m.client.Get(ctx, key, actuation); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get transition attempt NodeActuation %s: %w", key, err)
	}
	if !metav1.IsControlledBy(actuation, measurement) {
		return nil, errors.New("retained transition attempt NodeActuation has unexpected ownership")
	}
	return actuation, nil
}

func (m *AttemptMaterializer) confirmSource(
	ctx context.Context,
	prepared PreparedMeasurement,
	directionIndex int,
) (bool, string, error) {
	actuations := &edgev1alpha1.NodeActuationList{}
	if err := m.client.List(ctx, actuations, client.InNamespace(prepared.Namespace)); err != nil {
		return false, "", fmt.Errorf("list NodeActuations for transition source confirmation: %w", err)
	}
	var latest *edgev1alpha1.NodeActuation
	var latestAt time.Time
	for index := range actuations.Items {
		candidate := &actuations.Items[index]
		if candidate.Spec.Node != prepared.TargetNode {
			continue
		}
		terminalAt, terminal := actuationTerminalTime(candidate)
		if !terminal {
			return false, AttemptReasonSourceStateUnknown, nil
		}
		if latest == nil || terminalAt.After(latestAt) {
			latest, latestAt = candidate, terminalAt
			continue
		}
		if terminalAt.Equal(latestAt) {
			return false, AttemptReasonSourceStateUnknown, nil
		}
	}
	if latest == nil || latest.Status.Phase != edgev1alpha1.NodeActuationPhaseReady ||
		latest.Status.TerminalFailure != nil || latest.Status.TargetObservation == nil {
		return false, AttemptReasonSourceStateUnknown, nil
	}
	expectedState := prepared.Directions[directionIndex].FrozenContract.SourceState
	expectedActuator := prepared.Directions[1-directionIndex].Actuator
	if latest.Spec.DeviceClass != prepared.DeviceClass || latest.Spec.PowerMode != expectedState.PowerMode ||
		!samePowerModeActuator(latest.Spec.Actuator, expectedActuator) ||
		latest.Status.TargetObservation.PowerMode != expectedState.PowerMode ||
		!samePowerModeActuator(latest.Status.TargetObservation.Actuator, expectedActuator) {
		return false, AttemptReasonSourceStateMismatch, nil
	}
	return true, "", nil
}

func samePowerModeActuator(left, right edgev1alpha1.PowerModeActuator) bool {
	if left.Kind != right.Kind {
		return false
	}
	switch left.Kind {
	case edgev1alpha1.PowerModeActuatorFixed:
		return left.JetsonNVPModel == nil && right.JetsonNVPModel == nil &&
			left.NvidiaLockedGPUClock == nil && right.NvidiaLockedGPUClock == nil
	case edgev1alpha1.PowerModeActuatorJetsonNVPModel:
		return left.JetsonNVPModel != nil && right.JetsonNVPModel != nil &&
			left.JetsonNVPModel.ID == right.JetsonNVPModel.ID &&
			left.NvidiaLockedGPUClock == nil && right.NvidiaLockedGPUClock == nil
	case edgev1alpha1.PowerModeActuatorNvidiaLockedGPUClock:
		return left.NvidiaLockedGPUClock != nil && right.NvidiaLockedGPUClock != nil &&
			left.NvidiaLockedGPUClock.MinMHz == right.NvidiaLockedGPUClock.MinMHz &&
			left.NvidiaLockedGPUClock.MaxMHz == right.NvidiaLockedGPUClock.MaxMHz &&
			sameOptionalInt32(left.NvidiaLockedGPUClock.Mode, right.NvidiaLockedGPUClock.Mode) &&
			left.JetsonNVPModel == nil && right.JetsonNVPModel == nil
	default:
		return false
	}
}

func sameOptionalInt32(left, right *int32) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func actuationTerminalTime(actuation *edgev1alpha1.NodeActuation) (time.Time, bool) {
	if actuation == nil || actuation.Generation < 1 ||
		actuation.Status.ObservedGeneration != actuation.Generation {
		return time.Time{}, false
	}
	switch actuation.Status.Phase {
	case edgev1alpha1.NodeActuationPhaseReady:
		if actuation.Status.ReadyAt == nil || actuation.Status.ReadyAt.IsZero() ||
			actuation.Status.TargetObservation == nil || actuation.Status.TargetObservation.ObservedAt.IsZero() ||
			actuation.Status.ReadyAt.Before(&actuation.Status.TargetObservation.ObservedAt) {
			return time.Time{}, false
		}
		return actuation.Status.ReadyAt.Time, true
	case edgev1alpha1.NodeActuationPhaseFailed:
		if actuation.Status.TerminalFailure == nil || actuation.Status.TerminalFailure.OccurredAt.IsZero() {
			return time.Time{}, false
		}
		return actuation.Status.TerminalFailure.OccurredAt.Time, true
	default:
		return time.Time{}, false
	}
}

func buildAttemptActuation(
	measurement *edgev1alpha1.PowerModeTransitionCost,
	prepared PreparedMeasurement,
	selection attemptSelection,
	now time.Time,
	scheme *runtime.Scheme,
) (*edgev1alpha1.NodeActuation, error) {
	if scheme == nil {
		return nil, errors.New("Kubernetes scheme is required")
	}
	direction := prepared.Directions[selection.directionIndex]
	policy := direction.ActuationPolicy.Policy
	requestedAt := metav1.NewTime(now.UTC())
	actuation := &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{Namespace: measurement.Namespace, Name: selection.name},
		Spec: edgev1alpha1.NodeActuationSpec{
			Node: prepared.TargetNode, DeviceClass: prepared.DeviceClass,
			PowerMode: direction.FrozenContract.TargetState.PowerMode,
			Actuator:  *direction.Actuator.DeepCopy(), PolicyName: policy.Name,
			SettlingDurationSeconds:     int32(policy.SettlingDurationSeconds),
			MaxActuationDurationSeconds: int32(policy.MaxActuationDurationSeconds),
			StageRequestedAt:            requestedAt,
			Deadline: metav1.NewTime(requestedAt.Add(
				time.Duration(policy.MaxActuationDurationSeconds) * time.Second,
			)),
		},
	}
	if err := controllerutil.SetControllerReference(measurement, actuation, scheme); err != nil {
		return nil, fmt.Errorf("set transition attempt NodeActuation owner: %w", err)
	}
	return actuation, nil
}

func progressForRetainedAttempt(
	job *batchv1.Job,
	actuation *edgev1alpha1.NodeActuation,
) AttemptProgress {
	progress := AttemptProgress{Stage: AttemptStageActuationRunning, Job: job, NodeActuation: actuation}
	if (jobFailed(job) || jobComplete(job)) &&
		(actuation.Status.Phase == edgev1alpha1.NodeActuationPhaseReady ||
			actuation.Status.Phase == edgev1alpha1.NodeActuationPhaseFailed) {
		progress.Stage = AttemptStageFinalizationReady
		return progress
	}
	if jobFailed(job) {
		progress.Stage, progress.Reason = AttemptStageTerminal, AttemptReasonObserverFailed
		return progress
	}
	if jobComplete(job) {
		progress.Stage, progress.Reason = AttemptStageTerminal, AttemptReasonObserverEndedEarly
	}
	return progress
}

func jobComplete(job *batchv1.Job) bool { return hasJobCondition(job, batchv1.JobComplete) }

func jobFailed(job *batchv1.Job) bool { return hasJobCondition(job, batchv1.JobFailed) }

func hasJobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) bool {
	if job == nil {
		return false
	}
	for index := range job.Status.Conditions {
		condition := job.Status.Conditions[index]
		if condition.Type == conditionType && condition.Status == "True" {
			return true
		}
	}
	return false
}
