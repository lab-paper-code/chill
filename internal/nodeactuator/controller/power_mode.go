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

// nodePatch collects label and annotation changes; nil removes a key.
type nodePatch struct {
	labels      map[string]*string
	annotations map[string]*string
}

func (p *nodePatch) label(key string, value *string)      { p.labels[key] = value }
func (p *nodePatch) annotation(key string, value *string) { p.annotations[key] = value }

func (r *PowerModeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	node := &corev1.Node{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: r.Node}, node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	now := r.Now().UTC()
	patch := &nodePatch{labels: map[string]*string{}, annotations: map[string]*string{}}
	observation, observeErr := r.Host.Observe(ctx, nodeactuator.Request{
		Target: nodeactuator.Target{
			Kind: nodeactuator.ActuatorKindJetsonNVPModel, JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{},
		},
		Deadline: now.Add(r.Timeout),
	})
	if observeErr != nil {
		ctrl.LoggerFrom(ctx).Error(observeErr, "read back the host power mode", "node", r.Node)
	}
	generation, err := publishReadBack(node, observation, observeErr, patch)
	if err != nil {
		return ctrl.Result{}, err
	}
	result, err := r.drive(ctx, node, generation, observation, observeErr, now, patch)
	if patchErr := r.patch(ctx, patch); patchErr != nil {
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

// publishReadBack records the observed actuator and bootID and counts every
// change of either since the values on the Node, so a change by hand, a
// reboot, and an apply all count once, and an agent restart counts none. A
// host that cannot be read has no read-back. It returns the new generation.
func publishReadBack(
	node *corev1.Node,
	observation nodeactuator.Observation,
	observeErr error,
	patch *nodePatch,
) (int64, error) {
	var readBack *string
	if observeErr == nil {
		raw, err := json.Marshal(projectAPITarget(observation.Target))
		if err != nil {
			return 0, err
		}
		value := string(raw)
		readBack = &value
	}
	boot := node.Status.NodeInfo.BootID
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
	patch.annotation(metadata.PowerModeGeneration, &value)
	patch.annotation(metadata.PowerActuator, readBack)
	patch.annotation(metadata.PowerModeBoot, &boot)
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
	patch *nodePatch,
) (ctrl.Result, error) {
	desired := node.Labels[metadata.PowerMode]
	state, hasState := node.Labels[metadata.PowerModeState]
	if desired == "" {
		if hasState {
			patch.label(metadata.PowerModeState, nil)
		}
		return ctrl.Result{}, nil
	}
	transition, _, err := metadata.NodePowerModeTransition(node)
	if err != nil {
		return ctrl.Result{}, err
	}
	// The state label describes the transition's mode only.
	if transition.PowerMode != desired {
		state = ""
	}
	if state == metadata.PowerModeFailed {
		return ctrl.Result{}, nil
	}
	request, settling, failure := r.resolve(ctx, node, desired, now)
	if failure != "" {
		r.fail(patch, metadata.PowerModeTransitionRecord{PowerMode: desired, Generation: generation}, failure)
		return ctrl.Result{}, nil
	}
	if observeErr == nil && equality.Semantic.DeepEqual(
		projectAPITarget(observation.Target), projectAPITarget(request.Target)) {
		switch state {
		case metadata.PowerModePending:
			return r.settle(transition, generation, observation, settling, now, patch), nil
		case "":
			// Already in the mode: nothing to apply or settle. The record
			// names the mode, so the state describes it, and the read-back
			// and Ready times a measurement starts after.
			verifiedAt := observation.ObservedAt.UTC()
			r.writeTransition(patch, metadata.PowerModeTransitionRecord{
				PowerMode: desired, Generation: generation, VerifiedAt: &verifiedAt, ReadyAt: &now,
			}, metadata.PowerModeSuccess)
		}
		return ctrl.Result{}, nil
	}
	switch {
	case state == metadata.PowerModeSuccess:
		return ctrl.Result{}, nil
	case state != metadata.PowerModePending || transition.ApplyStartedAt == nil:
		// The fence: pending and applyStartedAt are durable before the host changes.
		r.writeTransition(patch, metadata.PowerModeTransitionRecord{
			PowerMode: desired, Generation: generation, ApplyStartedAt: &now,
		}, metadata.PowerModePending)
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	case generation != transition.Generation:
		// The host changed since the fence, a reboot included, without
		// reaching the target. Applying again could restart the Node again.
		r.fail(patch, transition, fmt.Sprintf("host power mode changed to generation %d without reaching %s", generation, desired))
		return ctrl.Result{}, nil
	}
	if err := r.Host.Apply(ctx, request, request.Target); err != nil {
		r.fail(patch, transition, err.Error())
		return ctrl.Result{}, nil
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
	patch *nodePatch,
) ctrl.Result {
	switch {
	case transition.VerifiedAt == nil:
		verifiedAt := observation.ObservedAt.UTC()
		transition.Generation, transition.VerifiedAt, transition.SettlingStartedAt = generation, &verifiedAt, &now
		r.writeTransition(patch, transition, metadata.PowerModePending)
		return ctrl.Result{RequeueAfter: settling}
	}
	if remaining := transition.SettlingStartedAt.Add(settling).Sub(now); remaining > 0 {
		return ctrl.Result{RequeueAfter: remaining}
	}
	transition.ReadyAt = &now
	r.writeTransition(patch, transition, metadata.PowerModeSuccess)
	return ctrl.Result{}
}

// resolve finds the desired mode's actuator target and policy, or a failure.
func (r *PowerModeReconciler) resolve(
	ctx context.Context,
	node *corev1.Node,
	desired string,
	now time.Time,
) (nodeactuator.Request, time.Duration, string) {
	className := node.Labels[metadata.DeviceClass]
	class := &edgev1alpha1.DeviceClass{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: className}, class); err != nil {
		return nodeactuator.Request{}, 0, fmt.Sprintf("get DeviceClass %q: %v", className, err)
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
				}, time.Duration(policy.SettlingDurationSeconds) * time.Second, ""
			}
		}
		return nodeactuator.Request{}, 0, fmt.Sprintf("no actuation policy authorizes %s mode %s", className, desired)
	}
	return nodeactuator.Request{}, 0, fmt.Sprintf("DeviceClass %q has no power mode %q", className, desired)
}

func (r *PowerModeReconciler) fail(patch *nodePatch, transition metadata.PowerModeTransitionRecord, failure string) {
	transition.Failure = truncateStatusMessage(failure)
	r.writeTransition(patch, transition, metadata.PowerModeFailed)
}

func (r *PowerModeReconciler) writeTransition(patch *nodePatch, transition metadata.PowerModeTransitionRecord, state string) {
	raw, err := json.Marshal(transition)
	if err != nil {
		panic(err) // a struct of strings, integers, and times always encodes
	}
	value := string(raw)
	patch.annotation(metadata.PowerModeTransition, &value)
	patch.label(metadata.PowerModeState, &state)
}

func (r *PowerModeReconciler) patch(ctx context.Context, patch *nodePatch) error {
	if len(patch.labels) == 0 && len(patch.annotations) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"labels": patch.labels, "annotations": patch.annotations,
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
