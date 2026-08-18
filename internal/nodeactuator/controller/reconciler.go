package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maximumStatusMessage = 1024

type LeaseController interface {
	Observe(context.Context, nodeactuator.Request, time.Time) (LeaseRecord, error)
	AcquireOrRenew(context.Context, nodeactuator.Request, time.Time) (LeaseRecord, error)
	Release(context.Context, nodeactuator.Request) (bool, error)
}

type WorkloadSafetyChecker interface {
	Check(context.Context, nodeactuator.Request) (WorkloadSafetyResult, error)
}

type HostActuator interface {
	Observe(context.Context, nodeactuator.Request) (nodeactuator.Observation, error)
	Apply(context.Context, nodeactuator.Request, nodeactuator.Target) error
}

type TransitionReconcilerOptions struct {
	AssignedNode    nodeactuator.ObjectIdentity
	AllowedPolicies []nodeactuator.ActuationPolicySpec
}

func (o TransitionReconcilerOptions) Validate() error {
	if err := o.AssignedNode.Validate(); err != nil {
		return fmt.Errorf("validate assigned Node: %w", err)
	}
	if len(o.AllowedPolicies) == 0 {
		return errors.New("at least one allowed actuation policy is required")
	}
	seen := make(map[string]struct{}, len(o.AllowedPolicies))
	for _, policy := range o.AllowedPolicies {
		if err := policy.Validate(); err != nil {
			return fmt.Errorf("validate allowed actuation policy: %w", err)
		}
		selector := policy.DeviceClass + "\x00" + policy.PowerMode + "\x00" + policy.ActuatorKind
		if _, duplicate := seen[selector]; duplicate {
			return fmt.Errorf("duplicate allowed actuation-policy selector %q", selector)
		}
		seen[selector] = struct{}{}
	}
	return nil
}

// TransitionReconciler is the only controller allowed to drive the host actuator
// port. The Profiler owns immutable transition intent; this reconciler owns
// observed status.
type TransitionReconciler struct {
	client.Client
	lease    LeaseController
	safety   WorkloadSafetyChecker
	actuator HostActuator
	options  TransitionReconcilerOptions
	now      func() time.Time
}

func NewTransitionReconciler(
	kubeClient client.Client,
	lease LeaseController,
	safety WorkloadSafetyChecker,
	actuator HostActuator,
	options TransitionReconcilerOptions,
) (*TransitionReconciler, error) {
	if kubeClient == nil || lease == nil || safety == nil || actuator == nil {
		return nil, errors.New("Kubernetes client, Lease, workload safety, and host actuator are required")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &TransitionReconciler{
		Client: kubeClient, lease: lease, safety: safety, actuator: actuator,
		options: TransitionReconcilerOptions{
			AssignedNode:    options.AssignedNode,
			AllowedPolicies: append([]nodeactuator.ActuationPolicySpec(nil), options.AllowedPolicies...),
		},
		now: time.Now,
	}, nil
}

func (r *TransitionReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		Named("node-actuation").
		For(&edgev1alpha1.NodeActuation{}).
		Complete(r)
}

func (r *TransitionReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	transition := &edgev1alpha1.NodeActuation{}
	if err := r.Get(ctx, request.NamespacedName, transition); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get NodeActuation %s: %w", request.NamespacedName, err)
	}
	if transition.Spec.Node.Name != r.options.AssignedNode.Name {
		return ctrl.Result{}, nil
	}
	if transition.Spec.Node.UID != r.options.AssignedNode.UID {
		return ctrl.Result{}, errors.New("NodeActuation targets this Node name with a different UID")
	}
	if !transition.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	actuationRequest, err := ProjectRequest(transition, r.options.AssignedNode)
	if err != nil {
		return ctrl.Result{}, err
	}
	now := r.now().UTC()
	if !allowsActuationPolicy(r.options.AllowedPolicies, actuationRequest) &&
		transition.Status.Phase != edgev1alpha1.NodeActuationPhaseReady &&
		transition.Status.TerminalFailure == nil {
		failure := nodeactuator.Failure{
			Stage: nodeactuator.FailureStagePolicy, Reason: "PolicyRejected",
			Message: "frozen actuation policy is not authorized by this NodeActuator",
		}
		return r.markFailed(ctx, transition, now, failure)
	}
	record, err := r.lease.Observe(ctx, actuationRequest, now)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("observe Node actuation Lease: %w", err)
	}
	leaseStatusUpdated, err := r.synchronizeLeaseStatus(ctx, transition, record)
	if err != nil {
		return ctrl.Result{}, err
	}
	if leaseStatusUpdated {
		return ctrl.Result{}, nil
	}
	snapshot, err := ProjectSnapshot(transition, r.options.AssignedNode, record, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	decision, err := nodeactuator.Decide(snapshot)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("decide NodeActuation %s lifecycle: %w", request.NamespacedName, err)
	}
	return r.execute(ctx, transition, snapshot, decision)
}

func allowsActuationPolicy(policies []nodeactuator.ActuationPolicySpec, request nodeactuator.Request) bool {
	for _, policy := range policies {
		if policy.DeviceClass == request.DeviceClass && policy.PowerMode == request.PowerMode &&
			policy.ActuatorKind == request.Target.Kind &&
			policy.SettlingDurationSeconds == int(request.SettlingDurationSeconds) &&
			policy.MaxActuationDurationSeconds == int(request.MaxActuationDurationSeconds) {
			return true
		}
	}
	return false
}

func (r *TransitionReconciler) synchronizeLeaseStatus(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	record LeaseRecord,
) (bool, error) {
	switch record.State {
	case nodeactuator.LockHeld:
		if record.Name == "" || record.UID == "" {
			return false, errors.New("held Node actuation Lease has incomplete identity")
		}
		want := edgev1alpha1.ProfilingRunObjectReference{Name: record.Name, UID: record.UID}
		if transition.Status.Lease == nil {
			if err := r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
				status.Lease = &want
			}); err != nil {
				return false, err
			}
			return true, nil
		}
		if *transition.Status.Lease != want {
			return false, errors.New("recorded Node actuation Lease identity differs from the held Lease")
		}
	case nodeactuator.LockNotHeld, nodeactuator.LockConflict:
		if transition.Status.Lease != nil {
			if err := r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
				status.Lease = nil
			}); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func (r *TransitionReconciler) execute(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	decision nodeactuator.Decision,
) (ctrl.Result, error) {
	switch decision.Action {
	case nodeactuator.ActionAcquireLease, nodeactuator.ActionRenewLease:
		return r.executeLeaseAction(ctx, transition, snapshot, decision)
	case nodeactuator.ActionWaitForLease, nodeactuator.ActionWaitForSettling:
		return ctrl.Result{RequeueAfter: decision.RequeueAfter}, nil
	case nodeactuator.ActionCheckSafety:
		return r.executeSafetyAction(ctx, transition, snapshot)
	case nodeactuator.ActionObservePrevious, nodeactuator.ActionApplyTarget,
		nodeactuator.ActionObserveTarget, nodeactuator.ActionStartSettling,
		nodeactuator.ActionMarkReady:
		return r.executeTargetAction(ctx, transition, snapshot, decision)
	case nodeactuator.ActionReleaseLease:
		return r.executeRelease(ctx, transition, snapshot)
	case nodeactuator.ActionMarkFailed:
		if decision.Failure == nil {
			return ctrl.Result{}, errors.New("failure action omitted typed failure")
		}
		return r.markFailed(ctx, transition, snapshot.Now, *decision.Failure)
	case nodeactuator.ActionComplete:
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported NodeActuation action %q", decision.Action)
	}
}

func (r *TransitionReconciler) executeLeaseAction(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	decision nodeactuator.Decision,
) (ctrl.Result, error) {
	record, err := r.lease.AcquireOrRenew(ctx, snapshot.Request, snapshot.Now)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("acquire or renew Node actuation Lease: %w", err)
	}
	if record.State != nodeactuator.LockHeld || record.Name == "" || record.UID == "" {
		return ctrl.Result{RequeueAfter: record.RetryAfter}, nil
	}
	err = r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Lease = &edgev1alpha1.ProfilingRunObjectReference{Name: record.Name, UID: record.UID}
		if decision.Action == nodeactuator.ActionAcquireLease && status.PreviousObservation == nil {
			status.Phase = edgev1alpha1.NodeActuationPhaseAcquiringLease
			status.Reason, status.Message = "LeaseHeld", "exact Node UID Lease is held by this transition"
		}
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *TransitionReconciler) executeSafetyAction(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	result, err := r.safety.Check(ctx, snapshot.Request)
	if err != nil {
		return r.failTerminally(ctx, transition, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageSafety, Reason: "WorkloadSafetyCheckFailed", Message: err.Error(),
		})
	}
	if err := result.Validate(); err != nil {
		return r.failTerminally(ctx, transition, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageSafety, Reason: "WorkloadSafetyResultInvalid", Message: err.Error(),
		})
	}
	err = r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Safety = projectSafetyResult(result)
		if status.Phase != edgev1alpha1.NodeActuationPhaseReady {
			status.Phase = edgev1alpha1.NodeActuationPhaseCheckingSafety
		}
		if result.Allowed {
			status.Reason, status.Message = "WorkloadSafetyAllowed", "all observed active Pods are explicitly allowed"
		} else {
			status.Reason, status.Message = "WorkloadSafetyBlocked", "an observed active Pod is not explicitly allowed"
		}
	})
	return ctrl.Result{}, err
}

func (r *TransitionReconciler) executeTargetAction(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	decision nodeactuator.Decision,
) (ctrl.Result, error) {
	switch decision.Action {
	case nodeactuator.ActionObservePrevious:
		return r.executeObservePrevious(ctx, transition, snapshot)
	case nodeactuator.ActionApplyTarget:
		return r.executeApplyTarget(ctx, transition, snapshot)
	case nodeactuator.ActionObserveTarget:
		return r.executeObserveTarget(ctx, transition, snapshot)
	case nodeactuator.ActionStartSettling:
		if snapshot.TargetObservation == nil && snapshot.PreviousObservation == nil {
			return ctrl.Result{}, errors.New("settling action omitted target evidence")
		}
		now := metav1.NewMicroTime(snapshot.Now)
		err := r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
			if snapshot.TargetObservation == nil {
				status.TargetObservation = projectHostObservation(*snapshot.PreviousObservation)
				verifiedAt := metav1.NewMicroTime(snapshot.PreviousObservation.ObservedAt)
				status.VerifiedAt = &verifiedAt
				status.Reason = "TargetAlreadyActive"
				status.Message = "current host state already matched the requested target; settling interval started"
			} else {
				status.Reason = "TargetVerified"
				status.Message = "target read-back matched; settling interval started"
			}
			status.Phase, status.SettlingStartedAt = edgev1alpha1.NodeActuationPhaseSettling, &now
		})
		return ctrl.Result{}, err
	case nodeactuator.ActionMarkReady:
		now := metav1.NewMicroTime(snapshot.Now)
		err := r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
			status.Phase, status.ReadyAt = edgev1alpha1.NodeActuationPhaseReady, &now
			if status.ApplyStartedAt != nil && status.SettlingStartedAt != nil {
				status.TransitionCost = &edgev1alpha1.NodeActuationTransitionCost{
					EndToEndMilliseconds: now.UnixMilli() - status.ApplyStartedAt.UnixMilli(),
					ApplyAndReadBackMilliseconds: status.SettlingStartedAt.UnixMilli() -
						status.ApplyStartedAt.UnixMilli(),
					SettlingMilliseconds: now.UnixMilli() - status.SettlingStartedAt.UnixMilli(),
				}
			}
			status.Reason, status.Message = "TargetSettled", "target was independently observed and settled"
		})
		return ctrl.Result{}, err
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported target action %q", decision.Action)
	}
}

func (r *TransitionReconciler) executeObservePrevious(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	observation, err := r.actuator.Observe(ctx, snapshot.Request)
	if err != nil {
		return r.failTerminally(ctx, transition, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageObservePrevious, Reason: "PreviousObservationFailed", Message: err.Error(),
		})
	}
	if err := observation.Validate(); err != nil {
		return r.failTerminally(ctx, transition, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageObservePrevious, Reason: "PreviousObservationInvalid", Message: err.Error(),
		})
	}
	err = r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Phase = edgev1alpha1.NodeActuationPhaseObservingPrevious
		status.PreviousObservation = projectHostObservation(observation)
		status.Reason, status.Message = "PreviousStateObserved", "current host state is retained before the target-state decision"
	})
	return ctrl.Result{}, err
}

func (r *TransitionReconciler) executeApplyTarget(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	if transition.Status.Phase != edgev1alpha1.NodeActuationPhaseApplying {
		now := metav1.NewMicroTime(snapshot.Now)
		err := r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
			status.Phase = edgev1alpha1.NodeActuationPhaseApplying
			if status.ApplyStartedAt == nil {
				status.ApplyStartedAt = &now
			}
			status.Reason, status.Message = "ApplyingTarget", "target application is durably fenced before host mutation"
		})
		return ctrl.Result{}, err
	}
	if err := r.actuator.Apply(ctx, snapshot.Request, snapshot.Request.Target); err != nil {
		return r.failTerminally(ctx, transition, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageApply, Reason: "TargetApplyFailed", Message: err.Error(),
		})
	}
	err := r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Phase = edgev1alpha1.NodeActuationPhaseVerifying
		status.Reason, status.Message = "TargetApplied", "host command completed; independent read-back is still required"
	})
	return ctrl.Result{}, err
}

func (r *TransitionReconciler) executeObserveTarget(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	observation, err := r.actuator.Observe(ctx, snapshot.Request)
	if err != nil {
		return r.failTerminally(ctx, transition, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageVerify, Reason: "TargetObservationFailed", Message: err.Error(),
		})
	}
	if err := observation.Validate(); err != nil {
		return r.failTerminally(ctx, transition, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageVerify, Reason: "TargetObservationInvalid", Message: err.Error(),
		})
	}
	now := metav1.NewMicroTime(observation.ObservedAt)
	err = r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
		status.TargetObservation, status.VerifiedAt = projectHostObservation(observation), &now
		status.Reason, status.Message = "TargetObserved", "independent target read-back was retained"
	})
	return ctrl.Result{}, err
}

func (r *TransitionReconciler) executeRelease(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	if _, err := r.lease.Release(ctx, snapshot.Request); err != nil {
		return ctrl.Result{}, fmt.Errorf("release Node actuation Lease: %w", err)
	}
	err := r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Lease = nil
	})
	return ctrl.Result{}, err
}

func (r *TransitionReconciler) failTerminally(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	failure nodeactuator.Failure,
) (ctrl.Result, error) {
	return r.markFailed(ctx, transition, snapshot.Now, failure)
}

func (r *TransitionReconciler) markFailed(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	now time.Time,
	failure nodeactuator.Failure,
) (ctrl.Result, error) {
	err := r.patchStatus(ctx, transition, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Phase = edgev1alpha1.NodeActuationPhaseFailed
		setTerminalFailure(status, failure, now)
	})
	return ctrl.Result{}, err
}

func (r *TransitionReconciler) patchStatus(
	ctx context.Context,
	transition *edgev1alpha1.NodeActuation,
	mutate func(*edgev1alpha1.NodeActuationStatus),
) error {
	base := transition.DeepCopy()
	desired := transition.DeepCopy()
	mutate(&desired.Status)
	desired.Status.ObservedGeneration = desired.Generation
	if reflect.DeepEqual(base.Status, desired.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch NodeActuation status: %w", err)
	}
	transition.Status = desired.Status
	return nil
}

func projectSafetyResult(result WorkloadSafetyResult) *edgev1alpha1.NodeActuationSafetyObservation {
	verdict := edgev1alpha1.NodeActuationSafetyAllowed
	if !result.Allowed {
		verdict = edgev1alpha1.NodeActuationSafetyBlocked
	}
	projected := &edgev1alpha1.NodeActuationSafetyObservation{
		Verdict: verdict, ObservedAt: metav1.NewTime(result.ObservedAt),
		BlockingPods: make([]edgev1alpha1.NodeActuationPodReference, 0, len(result.BlockingPods)),
	}
	for _, pod := range result.BlockingPods {
		projected.BlockingPods = append(projected.BlockingPods, edgev1alpha1.NodeActuationPodReference{
			Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID,
		})
	}
	return projected
}

func projectHostObservation(observation nodeactuator.Observation) *edgev1alpha1.NodeActuationObservation {
	return &edgev1alpha1.NodeActuationObservation{
		PowerMode:  observation.PowerMode,
		Actuator:   projectAPITarget(observation.Target),
		ObservedAt: metav1.NewTime(observation.ObservedAt),
	}
}

func projectAPITarget(target nodeactuator.Target) edgev1alpha1.PowerModeActuator {
	projected := edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorKind(target.Kind)}
	if target.JetsonNVPModel != nil {
		projected.JetsonNVPModel = &edgev1alpha1.JetsonNVPModelTarget{ID: target.JetsonNVPModel.ID}
	}
	if target.NvidiaLockedGPUClock != nil {
		projected.NvidiaLockedGPUClock = &edgev1alpha1.NvidiaLockedGPUClockTarget{
			MinMHz: target.NvidiaLockedGPUClock.MinMHz,
			MaxMHz: target.NvidiaLockedGPUClock.MaxMHz,
			Mode:   target.NvidiaLockedGPUClock.Mode,
		}
	}
	return projected
}

func setTerminalFailure(
	status *edgev1alpha1.NodeActuationStatus,
	failure nodeactuator.Failure,
	now time.Time,
) {
	message := truncateStatusMessage(failure.Message)
	status.TerminalFailure = &edgev1alpha1.NodeActuationFailure{
		Stage:  edgev1alpha1.NodeActuationFailureStage(failure.Stage),
		Reason: failure.Reason, Message: message, OccurredAt: metav1.NewTime(now),
	}
	status.Reason, status.Message = failure.Reason, message
}

func truncateStatusMessage(message string) string {
	message = strings.TrimSpace(message)
	if utf8.RuneCountInString(message) <= maximumStatusMessage {
		return message
	}
	runes := []rune(message)
	return string(runes[:maximumStatusMessage])
}
