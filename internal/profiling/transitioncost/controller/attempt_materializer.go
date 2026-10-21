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
	"github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	Stage  AttemptStage
	Reason string
	Job    *batchv1.Job
}

// AttemptMaterializer advances exactly one caller-selected directed attempt
// within the enclosing PowerModeTransitionCost reconciler.
type AttemptMaterializer struct {
	client  client.Client
	nodes   client.Reader
	scheme  *runtime.Scheme
	stores  AttemptStoreBindingResolver
	options AttemptJobOptions
	now     func() time.Time
}

// NewAttemptMaterializer reads the target Node through nodes, uncached, so a
// power-mode request patches the Node version it checked.
func NewAttemptMaterializer(
	kubeClient client.Client,
	nodes client.Reader,
	scheme *runtime.Scheme,
	stores AttemptStoreBindingResolver,
	options AttemptJobOptions,
) (*AttemptMaterializer, error) {
	if kubeClient == nil || nodes == nil || scheme == nil || stores == nil {
		return nil, errors.New("Kubernetes client, Node reader, scheme, and Evidence Store resolver are required")
	}
	options.Resources = *options.Resources.DeepCopy()
	options.Tolerations = cloneTolerations(options.Tolerations)
	return &AttemptMaterializer{
		client: kubeClient, nodes: nodes, scheme: scheme, stores: stores, options: options, now: time.Now,
	}, nil
}

// Advance creates at most one observer Job and, once it is Ready, requests the
// target power mode on the Node. Repeated calls reconstruct progress from the
// immutable preparation, the owned Job, and the Node; they never select a
// later attempt.
func (m *AttemptMaterializer) Advance(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	prepared PreparedMeasurement,
	directionIndex int,
	attemptNumber int32,
) (AttemptProgress, error) {
	selection, err := selectAttempt(measurement, prepared.EvidenceStore, directionIndex, attemptNumber)
	if err != nil {
		return AttemptProgress{}, err
	}

	job, err := m.loadJob(ctx, measurement, selection)
	if err != nil {
		return AttemptProgress{}, err
	}
	node, err := profilingrun.GrantedNode(ctx, m.nodes, measurement.UID, prepared.TargetNode.Name, prepared.TargetNode.UID)
	if err != nil {
		return AttemptProgress{}, err
	}
	if job == nil {
		if confirmed, reason := m.confirmSource(node, prepared, directionIndex); !confirmed {
			return AttemptProgress{Stage: AttemptStageTerminal, Reason: reason}, nil
		}
		job, err = m.createOrRecoverObserverJob(ctx, measurement, prepared, selection)
		if err != nil {
			return AttemptProgress{}, err
		}
		return AttemptProgress{Stage: AttemptStageObserverStarting, Job: job}, nil
	}
	// The Job exists only after the source was confirmed, and this attempt
	// is the Node's only writer, so a target label is this attempt's request.
	direction := prepared.Directions[directionIndex]
	target := direction.FrozenContract.TargetState.PowerMode
	if node.Labels[metadata.PowerMode] == target {
		return progressForRetainedAttempt(job, node, direction, m.now()), nil
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

	if confirmed, reason := m.confirmSource(node, prepared, directionIndex); !confirmed {
		return AttemptProgress{Stage: AttemptStageTerminal, Reason: reason, Job: job}, nil
	}
	if err := metadata.RequestPowerMode(ctx, m.client, node, target, m.now()); err != nil {
		return AttemptProgress{}, err
	}
	return AttemptProgress{Stage: AttemptStageActuationRunning, Job: job}, nil
}

func (m *AttemptMaterializer) createOrRecoverObserverJob(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	prepared PreparedMeasurement,
	selection attemptSelection,
) (*batchv1.Job, error) {
	binding, err := m.stores.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve transition attempt Evidence Store binding: %w", err)
	}
	job, err := BuildAttemptJob(AttemptJobRequest{
		Measurement: measurement, Prepared: prepared, DirectionIndex: selection.directionIndex,
		Name: selection.name, RawLocation: selection.rawLocation,
		StoreBinding: binding,
	}, m.options, m.scheme)
	if err != nil {
		return nil, err
	}
	if err := m.client.Create(ctx, job); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create transition attempt Job %q: %w", selection.name, err)
		}
		return m.loadJob(ctx, measurement, selection)
	}
	return job, nil
}

type attemptSelection struct {
	directionIndex int
	name           string
	rawLocation    evidencestore.ObjectLocation
	finalLocation  evidencestore.ObjectLocation
}

func selectAttempt(
	measurement *edgev1alpha1.PowerModeTransitionCost,
	evidenceStore string,
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
		rawLocation:   evidencestore.ObjectLocation{Store: evidenceStore, Key: prefix + "raw.json"},
		finalLocation: evidencestore.ObjectLocation{Store: evidenceStore, Key: prefix + "final.json"},
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

// confirmSource reports whether the Node's agent settled the source mode and
// still reads back the source actuator.
func (m *AttemptMaterializer) confirmSource(
	node *corev1.Node,
	prepared PreparedMeasurement,
	directionIndex int,
) (bool, string) {
	toSource := prepared.Directions[1-directionIndex]
	settled, readsBack := metadata.HoldsPowerMode(
		node, prepared.Directions[directionIndex].FrozenContract.SourceState.PowerMode, toSource.Actuator,
		time.Duration(toSource.ActuationPolicy.Policy.MaxActuationDurationSeconds)*time.Second, m.now(),
	)
	switch {
	case !settled:
		return false, AttemptReasonSourceStateUnknown
	case !readsBack:
		return false, AttemptReasonSourceStateMismatch
	}
	return true, ""
}

// progressForRetainedAttempt follows a requested transition until both the
// observer Job and the Node's agent are done with it.
func progressForRetainedAttempt(
	job *batchv1.Job,
	node *corev1.Node,
	direction PreparedDirection,
	now time.Time,
) AttemptProgress {
	progress := AttemptProgress{Stage: AttemptStageActuationRunning, Job: job}
	done, err := metadata.PowerModeOutcome(
		node, direction.FrozenContract.TargetState.PowerMode,
		time.Duration(direction.ActuationPolicy.Policy.MaxActuationDurationSeconds)*time.Second, now,
	)
	if (jobFailed(job) || jobComplete(job)) && (done || err != nil) {
		progress.Stage = AttemptStageFinalizationReady
		return progress
	}
	if jobFailed(job) {
		progress.Stage, progress.Reason = AttemptStageTerminal, AttemptReasonObserverFailed
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
