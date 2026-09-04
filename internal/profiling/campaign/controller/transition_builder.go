package controller

import (
	"errors"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TransitionMaterial is the exact Node, target, and selected policy needed to
// create the one transition that establishes a campaign state.
type TransitionMaterial struct {
	Node                        edgev1alpha1.ProfilingRunObjectReference
	DeviceClass                 string
	PowerMode                   string
	Actuator                    edgev1alpha1.PowerModeActuator
	PolicyName                  string
	SettlingDurationSeconds     int32
	MaxActuationDurationSeconds int32
}

func projectTransitionMaterial(prepared profiler.PreparedRun) (*TransitionMaterial, error) {
	if err := prepared.ValidateActuationBinding(); err != nil {
		return nil, err
	}
	if prepared.RunConfig.Actuation == nil {
		return nil, nil
	}
	policy := prepared.ActuationPolicy.Policy
	return &TransitionMaterial{
		Node: edgev1alpha1.ProfilingRunObjectReference{
			Name: prepared.RunConfig.TargetNode.Name, UID: prepared.RunConfig.TargetNode.UID,
		},
		DeviceClass:                 prepared.RunConfig.State.DeviceClass,
		PowerMode:                   prepared.RunConfig.Actuation.PowerMode,
		Actuator:                    projectTransitionActuator(prepared.RunConfig.Actuation.Actuator),
		PolicyName:                  prepared.RunConfig.Actuation.PolicyName,
		SettlingDurationSeconds:     int32(policy.SettlingDurationSeconds),
		MaxActuationDurationSeconds: int32(policy.MaxActuationDurationSeconds),
	}, nil
}

func projectTransitionActuator(target profilingrun.ActuatorTarget) edgev1alpha1.PowerModeActuator {
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

// TransitionName is the deterministic name of a campaign's one transition.
func TransitionName(parentName string) string {
	return parentName + "-transition"
}

// BuildTransition assembles one immutable operation owned by its campaign.
func BuildTransition(
	parent *edgev1alpha1.BatchEnergyOptimization,
	material *TransitionMaterial,
	now time.Time,
) (*edgev1alpha1.NodeActuation, error) {
	if parent == nil || parent.UID == "" {
		return nil, errors.New("a BatchEnergyOptimization with a UID is required")
	}
	if material == nil {
		return nil, errors.New("transition material is required")
	}
	if material.Node.Name != parent.Spec.State.TargetNode ||
		material.DeviceClass != parent.Spec.State.Candidate.DeviceClass ||
		material.PowerMode != parent.Spec.State.Candidate.PowerMode {
		return nil, errors.New("transition material does not match the campaign state")
	}
	requestedAt := metav1.NewTime(now.UTC())
	transition := &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Name: TransitionName(parent.Name), Namespace: parent.Namespace,
			Labels: map[string]string{labelBatchEnergyOptimization: parent.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				parent, edgev1alpha1.GroupVersion.WithKind("BatchEnergyOptimization"),
			)},
		},
		Spec: edgev1alpha1.NodeActuationSpec{
			Node: material.Node, DeviceClass: material.DeviceClass, PowerMode: material.PowerMode,
			Actuator: material.Actuator, PolicyName: material.PolicyName,
			SettlingDurationSeconds:     material.SettlingDurationSeconds,
			MaxActuationDurationSeconds: material.MaxActuationDurationSeconds,
			StageRequestedAt:            requestedAt,
			Deadline: metav1.NewTime(now.UTC().Add(
				time.Duration(material.MaxActuationDurationSeconds) * time.Second,
			)),
		},
	}
	if transition.Name == "-transition" || transition.Spec.Node.UID == "" ||
		transition.Spec.PolicyName == "" || transition.Spec.MaxActuationDurationSeconds < 1 {
		return nil, errors.New("campaign transition material is incomplete")
	}
	return transition, nil
}
