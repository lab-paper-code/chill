package controller

import (
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestProjectTransitionMaterialKeepsTheFixedPathDirect(t *testing.T) {
	material, err := projectTransitionMaterial(profiler.PreparedRun{})
	if err != nil {
		t.Fatal(err)
	}
	if material != nil {
		t.Fatalf("fixed prepared Run projected transition material: %+v", material)
	}
}

func TestBuildTransitionOwnsOneRecordedCampaignOperation(t *testing.T) {
	parent := testParent()
	prepared := actuatedPreparedRun(parent)
	material, err := projectTransitionMaterial(prepared)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 14, 1, 2, 3, 0, time.UTC)
	transition, err := BuildTransition(parent, material, now)
	if err != nil {
		t.Fatal(err)
	}
	owner := metav1.GetControllerOf(transition)
	if transition.Name != TransitionName(parent.Name) || owner == nil || owner.UID != parent.UID {
		t.Fatalf("transition identity = %s owner=%#v", transition.Name, owner)
	}
	if transition.Spec.Node.Name != parent.Spec.State.TargetNode || transition.Spec.Node.UID != "node-uid" ||
		transition.Spec.DeviceClass != parent.Spec.State.Candidate.DeviceClass ||
		transition.Spec.PowerMode != parent.Spec.State.Candidate.PowerMode ||
		transition.Spec.PolicyName != "jetson-mode-v1" ||
		transition.Spec.SettlingDurationSeconds != 30 ||
		transition.Spec.MaxActuationDurationSeconds != 300 ||
		!transition.Spec.StageRequestedAt.Time.Equal(now) ||
		!transition.Spec.Deadline.Time.Equal(now.Add(300*time.Second)) {
		t.Fatalf("transition = %#v", transition)
	}
}

func actuatedPreparedRun(parent *edgev1alpha1.BatchEnergyOptimization) profiler.PreparedRun {
	target := profilingrun.ActuatorTarget{
		Kind:           profilingrun.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 4},
	}
	policy := profiler.FrozenActuationPolicy{
		SchemaVersion: profiler.FrozenActuationPolicySchemaVersion,
		Source: profiler.ActuationPolicySource{
			Kind: profiler.ConfigMapActuationPolicySourceKind, Namespace: parent.Namespace,
			Name: "facts", UID: "facts-uid", ResourceVersion: "1", Key: "actuation-policies.yaml",
		},
		Policy: profiler.ActuationPolicy{
			SchemaVersion: profiler.ActuationPolicySchemaVersion,
			ActuationPolicySpec: profiler.ActuationPolicySpec{
				Name: "jetson-mode-v1", DeviceClass: parent.Spec.State.Candidate.DeviceClass,
				PowerMode: parent.Spec.State.Candidate.PowerMode, ActuatorKind: target.Kind,
				SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300,
			},
		},
	}
	return profiler.PreparedRun{
		RunConfig: profilingrun.RunConfig{
			State: profilingrun.ExecutionState{
				DeviceClass: parent.Spec.State.Candidate.DeviceClass,
				PowerMode:   parent.Spec.State.Candidate.PowerMode,
			},
			TargetNode: profilingrun.NodeSnapshot{Name: parent.Spec.State.TargetNode, UID: "node-uid"},
			Actuation: &profilingrun.ActuationContract{
				PowerMode: parent.Spec.State.Candidate.PowerMode,
				Actuator:  target, PolicyName: policy.Policy.Name,
			},
		},
		ActuationPolicy: &policy,
	}
}
