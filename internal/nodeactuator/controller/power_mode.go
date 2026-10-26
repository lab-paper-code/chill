package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const maximumStatusMessage = 1024

// HostActuator applies and independently reads back the host power mode.
type HostActuator interface {
	Observe(context.Context, nodeactuator.Request) (nodeactuator.Observation, error)
	Apply(context.Context, nodeactuator.Request, nodeactuator.Target) error
}

// PowerModeReconciler is the only mutator of its Node's host power state. It
// applies the mode the Node's desired power-mode label names and publishes
// read-back, bootID, generation, state, and the last transition on the Node.
type PowerModeReconciler struct {
	Client client.Client
	// Reader reads DeviceClasses without a watch.
	Reader   client.Reader
	Host     HostActuator
	Node     string
	Policies []nodeactuator.ActuationPolicySpec
	// Interval is the host read-back period.
	Interval time.Duration
	// Timeout bounds one host read-back.
	Timeout time.Duration
	Now     func() time.Time
}

func (r *PowerModeReconciler) SetupWithManager(manager ctrl.Manager) error {
	own := predicate.NewPredicateFuncs(func(object client.Object) bool { return object.GetName() == r.Node })
	return ctrl.NewControllerManagedBy(manager).
		Named("power-mode").
		For(&corev1.Node{}, builder.WithPredicates(own,
			predicate.Or(predicate.LabelChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Complete(r)
}

func (r *PowerModeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	node := &corev1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: r.Node}, node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Label and annotation changes for one patch; nil removes a key.
	labels, annotations := map[string]*string{}, map[string]*string{}
	kind := nodeactuator.ActuatorKindJetsonNVPModel
	for _, policy := range r.Policies {
		if policy.DeviceClass == node.Labels[metadata.DeviceClass] {
			kind = policy.ActuatorKind
			break
		}
	}
	observation, observeErr := r.Host.Observe(ctx, nodeactuator.Request{
		Target:   nodeactuator.Target{Kind: kind},
		Deadline: r.Now().Add(r.Timeout),
	})
	// Read after the host read, so every time this pass records follows the
	// read-back (ADR 0020 order).
	now := r.Now().UTC()
	if observeErr != nil {
		ctrl.LoggerFrom(ctx).Error(observeErr, "read back the host power mode", "node", r.Node)
	}
	wait, err := r.awaitBootRead(ctx, node, observeErr, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	if wait {
		return ctrl.Result{RequeueAfter: r.Interval}, nil
	}
	generation, err := publishReadBack(node, observation, observeErr, annotations)
	if err != nil {
		return ctrl.Result{}, err
	}
	result, err := r.drive(ctx, node, generation, observation, observeErr, now, labels, annotations)
	if patchErr := r.patch(ctx, labels, annotations); patchErr != nil {
		return ctrl.Result{}, errors.Join(err, patchErr)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if result.RequeueAfter == 0 || result.RequeueAfter > r.Interval {
		result.RequeueAfter = r.Interval
	}
	return result, nil
}

// awaitBootRead reports whether a pending request waits for a good host read:
// the host booted since the read-back, as a reboot mode does, and cannot be
// read yet. Only a read tells whether the boot reached the target, so the
// boot counts together with that read, unless the policy bound since the
// fence passes first.
func (r *PowerModeReconciler) awaitBootRead(
	ctx context.Context,
	node *corev1.Node,
	observeErr error,
	now time.Time,
) (bool, error) {
	desired := node.Labels[metadata.PowerMode]
	if observeErr == nil || node.Annotations[metadata.PowerModeBoot] == node.Status.NodeInfo.BootID ||
		node.Labels[metadata.PowerModeState] != metadata.PowerModePending {
		return false, nil
	}
	transition, _, err := metadata.NodePowerModeTransition(node)
	if err != nil || !metadata.ServesRequest(node, transition, desired) || transition.ApplyStartedAt == nil {
		return false, err
	}
	_, policy, failure, err := r.resolve(ctx, node, desired, now)
	if err != nil || failure != "" {
		return false, err
	}
	bound := time.Duration(policy.MaxActuationDurationSeconds) * time.Second
	return now.Before(transition.ApplyStartedAt.Add(bound)), nil
}

// publishReadBack records the observed actuator and bootID and counts every
// change of either since the values on the Node, so a change by hand, a
// reboot, and an apply all count once, and an agent restart counts none. A
// failed host read keeps the read-back and counts a new boot only; that boot
// clears the read-back, which no longer describes the host. The local Node is
// updated with the staged patch so a later read in this pass counts from it.
func publishReadBack(
	node *corev1.Node,
	observation nodeactuator.Observation,
	observeErr error,
	annotations map[string]*string,
) (int64, error) {
	boot := node.Status.NodeInfo.BootID
	var readBack *string
	if previous, ok := node.Annotations[metadata.PowerActuator]; ok && node.Annotations[metadata.PowerModeBoot] == boot {
		readBack = &previous
	}
	if observeErr == nil {
		raw, err := json.Marshal(projectAPITarget(observation.Target))
		if err != nil {
			return 0, err
		}
		value := string(raw)
		readBack = &value
	}
	generation, published, err := metadata.NodePowerModeGeneration(node)
	if err != nil {
		return 0, err
	}
	if published && sameAnnotation(node, metadata.PowerActuator, readBack) &&
		sameAnnotation(node, metadata.PowerModeBoot, &boot) {
		return generation, nil
	}
	if published {
		generation++
	}
	value := strconv.FormatInt(generation, 10)
	annotations[metadata.PowerModeGeneration] = &value
	annotations[metadata.PowerActuator] = readBack
	annotations[metadata.PowerModeBoot] = &boot
	if node.Annotations == nil {
		node.Annotations = make(map[string]string, 3)
	}
	node.Annotations[metadata.PowerModeGeneration] = value
	node.Annotations[metadata.PowerModeBoot] = boot
	if readBack == nil {
		delete(node.Annotations, metadata.PowerActuator)
	} else {
		node.Annotations[metadata.PowerActuator] = *readBack
	}
	return generation, nil
}

func sameAnnotation(node *corev1.Node, key string, want *string) bool {
	value, ok := node.Annotations[key]
	if want == nil {
		return !ok
	}
	return ok && value == *want
}

// drive moves the Node toward its desired mode. The agent never re-applies a
// mode the host left after success: the read-back is the actual mode.
func (r *PowerModeReconciler) drive(
	ctx context.Context,
	node *corev1.Node,
	generation int64,
	observation nodeactuator.Observation,
	observeErr error,
	now time.Time,
	labels, annotations map[string]*string,
) (ctrl.Result, error) {
	desired := node.Labels[metadata.PowerMode]
	state, hasState := node.Labels[metadata.PowerModeState]
	if desired == "" {
		if hasState {
			labels[metadata.PowerModeState] = nil
		}
		return ctrl.Result{}, nil
	}
	transition, _, err := metadata.NodePowerModeTransition(node)
	if err != nil {
		return ctrl.Result{}, err
	}
	// The state label describes the record of the current request only; a
	// new request time starts a new record even for the same mode.
	if !metadata.ServesRequest(node, transition, desired) {
		state = ""
	}
	requested := node.Annotations[metadata.PowerModeRequestedAt]
	if state == metadata.PowerModeFailed {
		return ctrl.Result{}, nil
	}
	request, policy, failure, err := r.resolve(ctx, node, desired, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	if failure != "" {
		r.fail(labels, annotations, metadata.PowerModeTransitionRecord{PowerMode: desired, RequestedAt: requested, Generation: generation}, failure)
		return ctrl.Result{}, nil
	}
	clockTarget := request.Target.Kind == nodeactuator.ActuatorKindNvidiaLockedGPUClock
	// Current MHz can coincide with intent under dynamic clocks. A new clock
	// request must apply the lock before a verified/settling record exists.
	needsClockApply := clockTarget && (state == "" ||
		state == metadata.PowerModePending && transition.VerifiedAt == nil)
	if !needsClockApply && observeErr == nil && equality.Semantic.DeepEqual(
		projectAPITarget(observation.Target), projectAPITarget(request.Target)) {
		switch state {
		case metadata.PowerModePending:
			settling := time.Duration(policy.SettlingDurationSeconds) * time.Second
			return r.settle(transition, generation, observation, settling, now, labels, annotations), nil
		case "":
			// Already in the mode: nothing to apply or settle. The record
			// names the mode, so the state describes it, and the read-back
			// and Ready times a measurement starts after.
			verifiedAt := observation.ObservedAt.UTC()
			r.writeTransition(labels, annotations, metadata.PowerModeTransitionRecord{
				PowerMode: desired, RequestedAt: requested, Generation: generation, VerifiedAt: &verifiedAt, ReadyAt: &now,
			}, metadata.PowerModeSuccess)
		}
		return ctrl.Result{}, nil
	}
	switch {
	case state == metadata.PowerModeSuccess:
		return ctrl.Result{}, nil
	case state != metadata.PowerModePending || transition.ApplyStartedAt == nil:
		// The fence: pending and applyStartedAt are durable before the host changes.
		r.writeTransition(labels, annotations, metadata.PowerModeTransitionRecord{
			PowerMode: desired, RequestedAt: requested, Generation: generation, ApplyStartedAt: &now,
		}, metadata.PowerModePending)
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	case generation != transition.Generation && !needsClockApply:
		// The host changed since the fence, a reboot included, without
		// reaching the target. Applying again could restart the Node again.
		r.fail(labels, annotations, transition, fmt.Sprintf("host power mode changed to generation %d without reaching %s", generation, desired))
		return ctrl.Result{}, nil
	}
	if err := r.Host.Apply(ctx, request, request.Target); err != nil {
		r.fail(labels, annotations, transition, err.Error())
		return ctrl.Result{}, nil
	}
	if clockTarget {
		observed, err := r.Host.Observe(ctx, request)
		if err != nil || !equality.Semantic.DeepEqual(projectAPITarget(observed.Target), projectAPITarget(request.Target)) {
			r.fail(labels, annotations, transition, fmt.Sprintf("NVIDIA post-apply read-back mismatch: %v", err))
			return ctrl.Result{}, nil
		}
		generation, err := publishReadBack(node, observed, nil, annotations)
		if err != nil {
			return ctrl.Result{}, err
		}
		return r.settle(transition, generation, observed,
			time.Duration(policy.SettlingDurationSeconds)*time.Second, r.Now().UTC(), labels, annotations), nil
	}
	return ctrl.Result{RequeueAfter: time.Millisecond}, nil
}

// settle finishes a pending transition whose target the host reads back.
func (r *PowerModeReconciler) settle(
	transition metadata.PowerModeTransitionRecord,
	generation int64,
	observation nodeactuator.Observation,
	settling time.Duration,
	now time.Time,
	labels, annotations map[string]*string,
) ctrl.Result {
	switch {
	case transition.VerifiedAt == nil:
		verifiedAt := observation.ObservedAt.UTC()
		transition.Generation, transition.VerifiedAt, transition.SettlingStartedAt = generation, &verifiedAt, &now
		r.writeTransition(labels, annotations, transition, metadata.PowerModePending)
		return ctrl.Result{RequeueAfter: settling}
	}
	if remaining := transition.SettlingStartedAt.Add(settling).Sub(now); remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}
	}
	transition.ReadyAt = &now
	r.writeTransition(labels, annotations, transition, metadata.PowerModeSuccess)
	return ctrl.Result{}
}

// resolve finds the desired mode's request and the policy that authorizes
// it, or a failure the request cannot outlive. A DeviceClass read that fails
// otherwise is an error, which requeues.
func (r *PowerModeReconciler) resolve(
	ctx context.Context,
	node *corev1.Node,
	desired string,
	now time.Time,
) (nodeactuator.Request, nodeactuator.ActuationPolicySpec, string, error) {
	className := node.Labels[metadata.DeviceClass]
	class := &edgev1alpha1.DeviceClass{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: className}, class); err != nil {
		if apierrors.IsNotFound(err) {
			return nodeactuator.Request{}, nodeactuator.ActuationPolicySpec{}, fmt.Sprintf("DeviceClass %q does not exist", className), nil
		}
		return nodeactuator.Request{}, nodeactuator.ActuationPolicySpec{}, "", fmt.Errorf("get DeviceClass %q: %w", className, err)
	}
	for _, mode := range class.Spec.PowerModes {
		if mode.Name != desired {
			continue
		}
		target := projectTarget(mode.Actuator)
		for _, policy := range r.Policies {
			if policy.DeviceClass == className && policy.PowerMode == desired && policy.ActuatorKind == target.Kind {
				return nodeactuator.Request{
					PowerMode: desired, Target: target,
					Deadline: now.Add(time.Duration(policy.MaxActuationDurationSeconds) * time.Second),
				}, policy, "", nil
			}
		}
		return nodeactuator.Request{}, nodeactuator.ActuationPolicySpec{}, fmt.Sprintf("no actuation policy authorizes %s mode %s", className, desired), nil
	}
	return nodeactuator.Request{}, nodeactuator.ActuationPolicySpec{}, fmt.Sprintf("DeviceClass %q has no power mode %q", className, desired), nil
}

func (r *PowerModeReconciler) fail(labels, annotations map[string]*string, transition metadata.PowerModeTransitionRecord, failure string) {
	transition.Failure = truncateStatusMessage(failure)
	r.writeTransition(labels, annotations, transition, metadata.PowerModeFailed)
}

func (r *PowerModeReconciler) writeTransition(labels, annotations map[string]*string, transition metadata.PowerModeTransitionRecord, state string) {
	raw, err := json.Marshal(transition)
	if err != nil {
		panic(err) // a struct of strings, integers, and times always encodes
	}
	value := string(raw)
	annotations[metadata.PowerModeTransition] = &value
	labels[metadata.PowerModeState] = &state
}

func (r *PowerModeReconciler) patch(ctx context.Context, labels, annotations map[string]*string) error {
	if len(labels) == 0 && len(annotations) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"labels": labels, "annotations": annotations,
	}})
	if err != nil {
		return err
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: r.Node}}
	if err := r.Client.Patch(ctx, node, client.RawPatch(types.MergePatchType, raw)); err != nil {
		return fmt.Errorf("patch Node %s power-mode metadata: %w", r.Node, err)
	}
	return nil
}

func projectTarget(actuator edgev1alpha1.PowerModeActuator) nodeactuator.Target {
	target := nodeactuator.Target{Kind: string(actuator.Kind)}
	if actuator.JetsonNVPModel != nil {
		target.JetsonNVPModel = &nodeactuator.JetsonNVPModelTarget{ID: actuator.JetsonNVPModel.ID}
	}
	if actuator.NvidiaLockedGPUClock != nil {
		target.NvidiaLockedGPUClock = &nodeactuator.NvidiaLockedGPUClockTarget{
			MinMHz: actuator.NvidiaLockedGPUClock.MinMHz, MaxMHz: actuator.NvidiaLockedGPUClock.MaxMHz,
			Mode: actuator.NvidiaLockedGPUClock.Mode,
		}
	}
	return target
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

func truncateStatusMessage(message string) string {
	message = strings.TrimSpace(message)
	if utf8.RuneCountInString(message) <= maximumStatusMessage {
		return message
	}
	runes := []rune(message)
	return string(runes[:maximumStatusMessage])
}
