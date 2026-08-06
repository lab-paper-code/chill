package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	nodeActuationFinalizer = "edge.dacs.io/node-actuation-restore"
	maximumStatusMessage   = 1024
)

type LeaseController interface {
	Observe(context.Context, nodeactuator.Request, time.Time) (LeaseBinding, error)
	AcquireOrRenew(context.Context, nodeactuator.Request, time.Time) (LeaseBinding, error)
	Release(context.Context, nodeactuator.Request) (bool, error)
}

type WorkloadSafetyChecker interface {
	Check(context.Context, nodeactuator.Request) (WorkloadSafetyResult, error)
}

type HostActuator interface {
	Observe(context.Context, nodeactuator.Request) (nodeactuator.Observation, error)
	Apply(context.Context, nodeactuator.Request, nodeactuator.Target) error
}

type HandoffReconcilerOptions struct {
	AssignedNode         nodeactuator.ObjectIdentity
	AllowedPolicyDigests []string
	SafetyValidFor       time.Duration
}

func (o HandoffReconcilerOptions) Validate() error {
	if err := o.AssignedNode.Validate(); err != nil {
		return fmt.Errorf("validate assigned Node: %w", err)
	}
	if o.SafetyValidFor < time.Second || o.SafetyValidFor > 5*time.Minute {
		return errors.New("workload safety validity must be between 1 second and 5 minutes")
	}
	if len(o.AllowedPolicyDigests) == 0 {
		return errors.New("at least one allowed actuation-policy digest is required")
	}
	seen := make(map[string]struct{}, len(o.AllowedPolicyDigests))
	for _, digest := range o.AllowedPolicyDigests {
		if err := nodeactuator.ValidatePolicyDigest(digest); err != nil {
			return fmt.Errorf("validate allowed actuation-policy digest: %w", err)
		}
		if _, duplicate := seen[digest]; duplicate {
			return fmt.Errorf("duplicate allowed actuation-policy digest %q", digest)
		}
		seen[digest] = struct{}{}
	}
	return nil
}

// HandoffReconciler is the only controller allowed to drive the host actuator
// port. The Profiler owns desired stage; this reconciler owns observed status.
type HandoffReconciler struct {
	client.Client
	lease    LeaseController
	safety   WorkloadSafetyChecker
	actuator HostActuator
	options  HandoffReconcilerOptions
	now      func() time.Time
}

func NewHandoffReconciler(
	kubeClient client.Client,
	lease LeaseController,
	safety WorkloadSafetyChecker,
	actuator HostActuator,
	options HandoffReconcilerOptions,
) (*HandoffReconciler, error) {
	if kubeClient == nil || lease == nil || safety == nil || actuator == nil {
		return nil, errors.New("Kubernetes client, Lease, workload safety, and host actuator are required")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &HandoffReconciler{
		Client: kubeClient, lease: lease, safety: safety, actuator: actuator,
		options: HandoffReconcilerOptions{
			AssignedNode: options.AssignedNode,
			AllowedPolicyDigests: append(
				[]string(nil), options.AllowedPolicyDigests...,
			),
			SafetyValidFor: options.SafetyValidFor,
		},
		now: time.Now,
	}, nil
}

func (r *HandoffReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		Named("node-actuation").
		For(&edgev1alpha1.NodeActuation{}).
		Complete(r)
}

func (r *HandoffReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	handoff := &edgev1alpha1.NodeActuation{}
	if err := r.Get(ctx, request.NamespacedName, handoff); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get NodeActuation %s: %w", request.NamespacedName, err)
	}
	if handoff.Spec.Node.Name != r.options.AssignedNode.Name {
		return ctrl.Result{}, nil
	}
	if handoff.Spec.Node.UID != r.options.AssignedNode.UID {
		return ctrl.Result{}, errors.New("NodeActuation targets this Node name with a different UID")
	}
	if handoff.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(handoff, nodeActuationFinalizer) {
		base := handoff.DeepCopy()
		controllerutil.AddFinalizer(handoff, nodeActuationFinalizer)
		if err := r.Patch(ctx, handoff, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("add NodeActuation restoration finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}
	if !handoff.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(handoff, nodeActuationFinalizer) {
		return ctrl.Result{}, nil
	}

	actuationRequest, err := ProjectRequest(handoff, r.options.AssignedNode)
	if err != nil {
		return ctrl.Result{}, err
	}
	now := r.now().UTC()
	if !slices.Contains(r.options.AllowedPolicyDigests, actuationRequest.PolicyDigest) &&
		handoff.Status.TerminalFailure == nil {
		failure := nodeactuator.Failure{
			Stage: nodeactuator.FailureStagePolicy, Reason: "PolicyDigestRejected",
			Message: "frozen actuation-policy digest is not authorized by this NodeActuator",
		}
		if mayHaveMutated(handoff.Status) || handoff.Status.PreviousObservation != nil {
			return ctrl.Result{}, r.beginRestore(ctx, handoff, now, &failure)
		}
		return r.markFailed(ctx, handoff, now, failure)
	}
	binding, err := r.lease.Observe(ctx, actuationRequest, now)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("observe Node actuation Lease: %w", err)
	}
	leaseStatusUpdated, err := r.synchronizeLeaseStatus(ctx, handoff, binding)
	if err != nil {
		return ctrl.Result{}, err
	}
	if leaseStatusUpdated {
		return ctrl.Result{}, nil
	}
	if !handoff.DeletionTimestamp.IsZero() && canDeleteWithoutRestoration(handoff) {
		if binding.State == nodeactuator.LeaseHeld {
			if _, err := r.lease.Release(ctx, actuationRequest); err != nil {
				return ctrl.Result{}, fmt.Errorf("release unused Node actuation Lease: %w", err)
			}
		}
		return ctrl.Result{}, r.removeFinalizer(ctx, handoff)
	}
	snapshot, err := ProjectSnapshot(
		handoff, r.options.AssignedNode, binding, now, r.options.SafetyValidFor,
		!handoff.DeletionTimestamp.IsZero(),
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	decision, err := nodeactuator.Decide(snapshot)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("decide NodeActuation %s lifecycle: %w", request.NamespacedName, err)
	}
	return r.execute(ctx, handoff, snapshot, decision)
}

func (r *HandoffReconciler) synchronizeLeaseStatus(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	binding LeaseBinding,
) (bool, error) {
	switch binding.State {
	case nodeactuator.LeaseHeld:
		if binding.Name == "" || binding.UID == "" {
			return false, errors.New("held Node actuation Lease has incomplete identity")
		}
		want := edgev1alpha1.ProfilingRunObjectReference{Name: binding.Name, UID: binding.UID}
		if handoff.Status.Lease == nil {
			if err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
				status.Lease = &want
			}); err != nil {
				return false, err
			}
			return true, nil
		}
		if *handoff.Status.Lease != want {
			return false, errors.New("recorded Node actuation Lease identity differs from the held Lease")
		}
	case nodeactuator.LeaseNotHeld, nodeactuator.LeaseConflict:
		if handoff.Status.Lease != nil {
			if err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
				status.Lease = nil
			}); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func (r *HandoffReconciler) execute(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	decision nodeactuator.Decision,
) (ctrl.Result, error) {
	switch decision.Action {
	case nodeactuator.ActionAcquireLease, nodeactuator.ActionRenewLease:
		return r.executeLeaseAction(ctx, handoff, snapshot, decision)
	case nodeactuator.ActionWaitForLease, nodeactuator.ActionWaitForSettling, nodeactuator.ActionHold:
		return ctrl.Result{RequeueAfter: decision.RequeueAfter}, nil
	case nodeactuator.ActionCheckSafety:
		return r.executeSafetyAction(ctx, handoff, snapshot)
	case nodeactuator.ActionObservePrevious, nodeactuator.ActionApplyTarget,
		nodeactuator.ActionObserveTarget, nodeactuator.ActionStartSettling,
		nodeactuator.ActionMarkReady:
		return r.executeTargetAction(ctx, handoff, snapshot, decision)
	case nodeactuator.ActionBeginRestore:
		return ctrl.Result{}, r.beginRestore(ctx, handoff, snapshot.Now, decision.Failure)
	case nodeactuator.ActionRestorePrevious, nodeactuator.ActionObserveRestored,
		nodeactuator.ActionMarkRestored:
		return r.executeRestoreAction(ctx, handoff, snapshot, decision)
	case nodeactuator.ActionReleaseLease:
		return r.executeRelease(ctx, handoff, snapshot)
	case nodeactuator.ActionMarkFailed:
		if decision.Failure == nil {
			return ctrl.Result{}, errors.New("failure action omitted typed failure")
		}
		return r.markFailed(ctx, handoff, snapshot.Now, *decision.Failure)
	case nodeactuator.ActionComplete:
		if !handoff.DeletionTimestamp.IsZero() {
			return ctrl.Result{}, r.removeFinalizer(ctx, handoff)
		}
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported NodeActuation action %q", decision.Action)
	}
}

func (r *HandoffReconciler) executeLeaseAction(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	decision nodeactuator.Decision,
) (ctrl.Result, error) {
	binding, err := r.lease.AcquireOrRenew(ctx, snapshot.Request, snapshot.Now)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("acquire or renew Node actuation Lease: %w", err)
	}
	if binding.State != nodeactuator.LeaseHeld || binding.Name == "" || binding.UID == "" {
		return ctrl.Result{RequeueAfter: binding.RetryAfter}, nil
	}
	err = r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Lease = &edgev1alpha1.ProfilingRunObjectReference{Name: binding.Name, UID: binding.UID}
		if decision.Action == nodeactuator.ActionAcquireLease {
			status.Phase = edgev1alpha1.NodeActuationPhaseAcquiringLease
			status.Reason, status.Message = "LeaseHeld", "exact Node UID Lease is held by this handoff"
		}
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: binding.RetryAfter}, nil
}

func (r *HandoffReconciler) executeSafetyAction(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	result, err := r.safety.Check(ctx, snapshot.Request)
	if err != nil {
		return r.handlePortFailure(ctx, handoff, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageSafety, Reason: "WorkloadSafetyCheckFailed", Message: err.Error(),
		})
	}
	if err := result.Validate(); err != nil {
		return r.handlePortFailure(ctx, handoff, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageSafety, Reason: "WorkloadSafetyResultInvalid", Message: err.Error(),
		})
	}
	err = r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
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

func (r *HandoffReconciler) executeTargetAction(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	decision nodeactuator.Decision,
) (ctrl.Result, error) {
	switch decision.Action {
	case nodeactuator.ActionObservePrevious:
		return r.executeObservePrevious(ctx, handoff, snapshot)
	case nodeactuator.ActionApplyTarget:
		return r.executeApplyTarget(ctx, handoff, snapshot)
	case nodeactuator.ActionObserveTarget:
		return r.executeObserveTarget(ctx, handoff, snapshot)
	case nodeactuator.ActionStartSettling:
		now := metav1.NewTime(snapshot.Now)
		err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
			status.Phase, status.SettlingStartedAt = edgev1alpha1.NodeActuationPhaseSettling, &now
			status.Reason, status.Message = "TargetVerified", "target read-back matched; settling interval started"
		})
		return ctrl.Result{}, err
	case nodeactuator.ActionMarkReady:
		now := metav1.NewTime(snapshot.Now)
		err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
			status.Phase, status.ReadyAt = edgev1alpha1.NodeActuationPhaseReady, &now
			status.Reason, status.Message = "TargetSettled", "target was independently observed and settled"
		})
		return ctrl.Result{}, err
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported target action %q", decision.Action)
	}
}

func (r *HandoffReconciler) executeObservePrevious(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	observation, err := r.actuator.Observe(ctx, snapshot.Request)
	if err != nil {
		return r.handlePortFailure(ctx, handoff, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageObservePrevious, Reason: "PreviousObservationFailed", Message: err.Error(),
		})
	}
	if err := observation.Validate(); err != nil {
		return r.handlePortFailure(ctx, handoff, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageObservePrevious, Reason: "PreviousObservationInvalid", Message: err.Error(),
		})
	}
	err = r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Phase = edgev1alpha1.NodeActuationPhaseObservingPrevious
		status.PreviousObservation = projectHostObservation(observation)
		status.Reason, status.Message = "PreviousStateObserved", "previous host state is retained before mutation"
	})
	return ctrl.Result{}, err
}

func (r *HandoffReconciler) executeApplyTarget(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	if handoff.Status.Phase != edgev1alpha1.NodeActuationPhaseApplying {
		now := metav1.NewTime(snapshot.Now)
		err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
			status.Phase = edgev1alpha1.NodeActuationPhaseApplying
			if status.ApplyStartedAt == nil {
				status.ApplyStartedAt = &now
			}
			status.Reason, status.Message = "ApplyingTarget", "target application is durably fenced before host mutation"
		})
		return ctrl.Result{}, err
	}
	if err := r.actuator.Apply(ctx, snapshot.Request, snapshot.Request.Target); err != nil {
		return r.handlePortFailure(ctx, handoff, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageApply, Reason: "TargetApplyFailed", Message: err.Error(),
		})
	}
	err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Phase = edgev1alpha1.NodeActuationPhaseVerifying
		status.Reason, status.Message = "TargetApplied", "host command completed; independent read-back is still required"
	})
	return ctrl.Result{}, err
}

func (r *HandoffReconciler) executeObserveTarget(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	observation, err := r.actuator.Observe(ctx, snapshot.Request)
	if err != nil {
		return r.handlePortFailure(ctx, handoff, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageVerify, Reason: "TargetObservationFailed", Message: err.Error(),
		})
	}
	if err := observation.Validate(); err != nil {
		return r.handlePortFailure(ctx, handoff, snapshot, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageVerify, Reason: "TargetObservationInvalid", Message: err.Error(),
		})
	}
	now := metav1.NewTime(observation.ObservedAt)
	err = r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
		status.TargetObservation, status.VerifiedAt = projectHostObservation(observation), &now
		status.Reason, status.Message = "TargetObserved", "independent target read-back was retained"
	})
	return ctrl.Result{}, err
}

func (r *HandoffReconciler) executeRestoreAction(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	decision nodeactuator.Decision,
) (ctrl.Result, error) {
	switch decision.Action {
	case nodeactuator.ActionRestorePrevious:
		if snapshot.PreviousObservation == nil {
			return ctrl.Result{}, errors.New("restore action omitted previous observation")
		}
		if err := r.actuator.Apply(ctx, snapshot.Request, snapshot.PreviousObservation.Target); err != nil {
			return r.markFailed(ctx, handoff, snapshot.Now, nodeactuator.Failure{
				Stage: nodeactuator.FailureStageRestore, Reason: "RestoreApplyFailed", Message: err.Error(),
			})
		}
		now := metav1.NewTime(snapshot.Now)
		err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
			status.Phase, status.RestoreAppliedAt = edgev1alpha1.NodeActuationPhaseVerifyingRestore, &now
			status.Reason, status.Message = "PreviousStateApplied", "restore command completed; independent read-back is still required"
		})
		return ctrl.Result{}, err
	case nodeactuator.ActionObserveRestored:
		return r.executeObserveRestored(ctx, handoff, snapshot)
	case nodeactuator.ActionMarkRestored:
		now := metav1.NewTime(snapshot.Now)
		err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
			status.RestoredAt = &now
			if decision.Failure == nil {
				status.Phase = edgev1alpha1.NodeActuationPhaseRestored
				status.Reason, status.Message = "PreviousStateRestored", "previous state was independently observed after restoration"
			} else {
				status.Phase = edgev1alpha1.NodeActuationPhaseFailed
				setTerminalFailure(status, *decision.Failure, snapshot.Now)
			}
		})
		return ctrl.Result{}, err
	default:
		return ctrl.Result{}, fmt.Errorf("unsupported restore action %q", decision.Action)
	}
}

func (r *HandoffReconciler) executeObserveRestored(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	observation, err := r.actuator.Observe(ctx, snapshot.Request)
	if err != nil {
		return r.markFailed(ctx, handoff, snapshot.Now, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageRestore, Reason: "RestoreObservationFailed", Message: err.Error(),
		})
	}
	if err := observation.Validate(); err != nil {
		return r.markFailed(ctx, handoff, snapshot.Now, nodeactuator.Failure{
			Stage: nodeactuator.FailureStageRestore, Reason: "RestoreObservationInvalid", Message: err.Error(),
		})
	}
	err = r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
		status.RestoredObservation = projectHostObservation(observation)
		status.Reason, status.Message = "RestoredStateObserved", "independent restoration read-back was retained"
	})
	return ctrl.Result{}, err
}

func (r *HandoffReconciler) executeRelease(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
) (ctrl.Result, error) {
	if _, err := r.lease.Release(ctx, snapshot.Request); err != nil {
		return ctrl.Result{}, fmt.Errorf("release Node actuation Lease: %w", err)
	}
	err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Lease = nil
	})
	return ctrl.Result{}, err
}

func (r *HandoffReconciler) handlePortFailure(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	snapshot nodeactuator.Snapshot,
	failure nodeactuator.Failure,
) (ctrl.Result, error) {
	if mayHaveMutated(handoff.Status) {
		return ctrl.Result{}, r.beginRestore(ctx, handoff, snapshot.Now, &failure)
	}
	return r.markFailed(ctx, handoff, snapshot.Now, failure)
}

func (r *HandoffReconciler) beginRestore(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	now time.Time,
	failure *nodeactuator.Failure,
) error {
	timestamp := metav1.NewTime(now)
	return r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Phase = edgev1alpha1.NodeActuationPhaseRestoring
		if status.RestoreStartedAt == nil {
			status.RestoreStartedAt = &timestamp
		}
		status.Reason, status.Message = "RestorationStarted", "previous state restoration is required"
		if failure != nil {
			setTerminalFailure(status, *failure, now)
		}
	})
}

func (r *HandoffReconciler) markFailed(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	now time.Time,
	failure nodeactuator.Failure,
) (ctrl.Result, error) {
	err := r.patchStatus(ctx, handoff, func(status *edgev1alpha1.NodeActuationStatus) {
		status.Phase = edgev1alpha1.NodeActuationPhaseFailed
		setTerminalFailure(status, failure, now)
	})
	return ctrl.Result{}, err
}

func (r *HandoffReconciler) patchStatus(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
	mutate func(*edgev1alpha1.NodeActuationStatus),
) error {
	base := handoff.DeepCopy()
	desired := handoff.DeepCopy()
	mutate(&desired.Status)
	desired.Status.ObservedGeneration = desired.Generation
	if reflect.DeepEqual(base.Status, desired.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch NodeActuation status: %w", err)
	}
	handoff.Status = desired.Status
	return nil
}

func (r *HandoffReconciler) removeFinalizer(
	ctx context.Context,
	handoff *edgev1alpha1.NodeActuation,
) error {
	if !controllerutil.ContainsFinalizer(handoff, nodeActuationFinalizer) {
		return nil
	}
	base := handoff.DeepCopy()
	controllerutil.RemoveFinalizer(handoff, nodeActuationFinalizer)
	if err := r.Patch(ctx, handoff, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove NodeActuation restoration finalizer: %w", err)
	}
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

func mayHaveMutated(status edgev1alpha1.NodeActuationStatus) bool {
	return status.ApplyStartedAt != nil || status.Phase == edgev1alpha1.NodeActuationPhaseApplying ||
		status.Phase == edgev1alpha1.NodeActuationPhaseVerifying ||
		status.Phase == edgev1alpha1.NodeActuationPhaseSettling ||
		status.Phase == edgev1alpha1.NodeActuationPhaseReady ||
		status.Phase == edgev1alpha1.NodeActuationPhaseRestoring ||
		status.Phase == edgev1alpha1.NodeActuationPhaseVerifyingRestore
}

func canDeleteWithoutRestoration(handoff *edgev1alpha1.NodeActuation) bool {
	return handoff.Status.PreviousObservation == nil && !mayHaveMutated(handoff.Status)
}

func truncateStatusMessage(message string) string {
	message = strings.TrimSpace(message)
	if utf8.RuneCountInString(message) <= maximumStatusMessage {
		return message
	}
	runes := []rune(message)
	return string(runes[:maximumStatusMessage])
}
