package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const actuationHandoffNameHashCharacters = 32

// ActuationHandoffs owns only the durable Profiler-to-NodeActuator API object.
// It performs no host operation and never writes NodeActuation status.
type ActuationHandoffs struct {
	client client.Client
	scheme *runtime.Scheme
	now    func() time.Time
}

func NewActuationHandoffs(kubeClient client.Client, scheme *runtime.Scheme) (*ActuationHandoffs, error) {
	if kubeClient == nil || scheme == nil {
		return nil, errors.New("Kubernetes client and scheme are required")
	}
	return &ActuationHandoffs{client: kubeClient, scheme: scheme, now: time.Now}, nil
}

// Ensure creates the deterministic handoff once or verifies and reuses the
// existing object. Mutable status is deliberately ignored by this writer.
func (h *ActuationHandoffs) Ensure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
) (*edgev1alpha1.NodeActuation, bool, error) {
	if h == nil || h.client == nil || h.scheme == nil || h.now == nil {
		return nil, false, errors.New("actuation handoff repository is incomplete")
	}
	name, err := actuationHandoffName(run)
	if err != nil {
		return nil, false, err
	}
	key := types.NamespacedName{Namespace: run.Namespace, Name: name}
	existing := &edgev1alpha1.NodeActuation{}
	if err := h.client.Get(ctx, key, existing); err == nil {
		if err := verifyActuationHandoff(run, existing, frozen); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, false, fmt.Errorf("get NodeActuation %s: %w", key, err)
	}

	desired, err := buildActuationHandoff(run, frozen, h.scheme, h.now())
	if err != nil {
		return nil, false, err
	}
	if err := h.client.Create(ctx, desired); err == nil {
		if desired.UID == "" {
			return nil, false, errors.New("created NodeActuation has incomplete API identity")
		}
		return desired, true, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return nil, false, fmt.Errorf("create NodeActuation %s: %w", key, err)
	}
	if err := h.client.Get(ctx, key, existing); err != nil {
		return nil, false, fmt.Errorf("get concurrently created NodeActuation %s: %w", key, err)
	}
	if err := verifyActuationHandoff(run, existing, frozen); err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

// AdvanceToRestore performs the only mutable Profiler-owned handoff
// transition. An elapsed absolute deadline is left to the NodeActuator's
// fail-safe restoration path because the API contract forbids a request
// timestamp at or after that deadline.
func (h *ActuationHandoffs) AdvanceToRestore(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
	handoff *edgev1alpha1.NodeActuation,
) (bool, error) {
	if h == nil || h.client == nil || h.scheme == nil || h.now == nil {
		return false, errors.New("actuation handoff repository is incomplete")
	}
	if err := verifyActuationHandoff(run, handoff, frozen); err != nil {
		return false, err
	}
	if handoff.Spec.DesiredStage == edgev1alpha1.NodeActuationDesiredStageRestore {
		return false, nil
	}
	requestedAt := h.now().UTC()
	if !requestedAt.After(handoff.Spec.StageRequestedAt.Time) {
		requestedAt = handoff.Spec.StageRequestedAt.Add(time.Nanosecond)
	}
	if !requestedAt.Before(handoff.Spec.Deadline.Time) {
		return false, nil
	}
	base := handoff.DeepCopy()
	desired := handoff.DeepCopy()
	desired.Spec.DesiredStage = edgev1alpha1.NodeActuationDesiredStageRestore
	desired.Spec.StageRequestedAt = metav1.NewTime(requestedAt)
	if err := h.client.Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return false, fmt.Errorf("advance NodeActuation to restore: %w", err)
	}
	handoff.Spec = desired.Spec
	return true, nil
}

func buildActuationHandoff(
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
	scheme *runtime.Scheme,
	now time.Time,
) (*edgev1alpha1.NodeActuation, error) {
	name, err := actuationHandoffName(run)
	if err != nil {
		return nil, err
	}
	material, err := actuationHandoffMaterial(frozen)
	if err != nil {
		return nil, err
	}
	requestedAt := metav1.NewTime(now.UTC())
	handoff := &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: run.Namespace,
			Name:      name,
			Labels: map[string]string{
				"app.kubernetes.io/name":      component.DefaultSystemName,
				"app.kubernetes.io/component": component.Profiler,
			},
		},
		Spec: edgev1alpha1.NodeActuationSpec{
			Run: edgev1alpha1.ProfilingRunObjectReference{Name: run.Name, UID: string(run.UID)},
			Node: edgev1alpha1.ProfilingRunObjectReference{
				Name: material.intent.TargetNode.Name, UID: material.intent.TargetNode.UID,
			},
			DeviceClass:                 material.intent.State.DeviceClass,
			PowerMode:                   material.intent.Actuation.PowerMode,
			Actuator:                    projectActuatorTarget(material.intent.Actuation.Actuator),
			PolicyDigest:                material.intent.Actuation.PolicyDigest,
			SettlingDurationSeconds:     int32(material.settlingDurationSeconds),
			MaxActuationDurationSeconds: int32(material.maxActuationDurationSeconds),
			Deadline:                    metav1.NewTime(now.UTC().Add(time.Duration(material.maxActuationDurationSeconds) * time.Second)),
			DesiredStage:                edgev1alpha1.NodeActuationDesiredStageApply,
			StageRequestedAt:            requestedAt,
		},
	}
	if err := controllerutil.SetControllerReference(run, handoff, scheme); err != nil {
		return nil, fmt.Errorf("set NodeActuation owner: %w", err)
	}
	return handoff, nil
}

type handoffMaterial struct {
	intent                      profilingrun.Intent
	settlingDurationSeconds     int
	maxActuationDurationSeconds int
}

func actuationHandoffMaterial(frozen FrozenRun) (handoffMaterial, error) {
	if err := frozen.Validate(); err != nil {
		return handoffMaterial{}, fmt.Errorf("validate frozen Run for actuation handoff: %w", err)
	}
	if frozen.Prepared.Intent.Actuation == nil || frozen.Prepared.ActuationPolicy == nil {
		return handoffMaterial{}, errors.New("actuation handoff requires a frozen actuated Run")
	}
	policy := frozen.Prepared.ActuationPolicy.Policy
	return handoffMaterial{
		intent:                      frozen.Prepared.Intent,
		settlingDurationSeconds:     policy.SettlingDurationSeconds,
		maxActuationDurationSeconds: policy.MaxActuationDurationSeconds,
	}, nil
}

func verifyActuationHandoff(
	run *edgev1alpha1.ProfilingRun,
	handoff *edgev1alpha1.NodeActuation,
	frozen FrozenRun,
) error {
	if run == nil || handoff == nil {
		return errors.New("ProfilingRun and NodeActuation are required")
	}
	name, err := actuationHandoffName(run)
	if err != nil {
		return err
	}
	if handoff.Namespace != run.Namespace || handoff.Name != name || handoff.UID == "" ||
		!metav1.IsControlledBy(handoff, run) {
		return errors.New("existing NodeActuation has invalid API identity or ownership")
	}
	material, err := actuationHandoffMaterial(frozen)
	if err != nil {
		return err
	}
	want := edgev1alpha1.NodeActuationSpec{
		Run:                         edgev1alpha1.ProfilingRunObjectReference{Name: run.Name, UID: string(run.UID)},
		Node:                        edgev1alpha1.ProfilingRunObjectReference{Name: material.intent.TargetNode.Name, UID: material.intent.TargetNode.UID},
		DeviceClass:                 material.intent.State.DeviceClass,
		PowerMode:                   material.intent.Actuation.PowerMode,
		Actuator:                    projectActuatorTarget(material.intent.Actuation.Actuator),
		PolicyDigest:                material.intent.Actuation.PolicyDigest,
		SettlingDurationSeconds:     int32(material.settlingDurationSeconds),
		MaxActuationDurationSeconds: int32(material.maxActuationDurationSeconds),
	}
	got := handoff.Spec
	want.Deadline, want.StageRequestedAt, want.DesiredStage = got.Deadline, got.StageRequestedAt, got.DesiredStage
	if !reflect.DeepEqual(got, want) {
		return errors.New("existing NodeActuation does not match frozen Run input")
	}
	if got.DesiredStage != edgev1alpha1.NodeActuationDesiredStageApply &&
		got.DesiredStage != edgev1alpha1.NodeActuationDesiredStageRestore {
		return fmt.Errorf("existing NodeActuation has unsupported desired stage %q", got.DesiredStage)
	}
	if got.StageRequestedAt.IsZero() || got.Deadline.IsZero() || !got.StageRequestedAt.Before(&got.Deadline) {
		return errors.New("existing NodeActuation has invalid timing identity")
	}
	if got.DesiredStage == edgev1alpha1.NodeActuationDesiredStageApply {
		wantDeadline := got.StageRequestedAt.Add(time.Duration(got.MaxActuationDurationSeconds) * time.Second)
		if !got.Deadline.Time.Equal(wantDeadline) {
			return errors.New("existing NodeActuation deadline does not match frozen maximum duration")
		}
	}
	return nil
}

func projectActuatorTarget(target profilingrun.ActuatorTarget) edgev1alpha1.PowerModeActuator {
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

func actuationHandoffName(run *edgev1alpha1.ProfilingRun) (string, error) {
	if run == nil || run.Namespace == "" || run.Name == "" || run.UID == "" {
		return "", errors.New("ProfilingRun namespace, name, and UID are required")
	}
	sum := sha256.Sum256([]byte(run.UID))
	hash := hex.EncodeToString(sum[:])[:actuationHandoffNameHashCharacters]
	return "profilingrun-" + hash + "-actuation", nil
}
