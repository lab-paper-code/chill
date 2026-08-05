package controller

import (
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestProjectRequestBindsExactAssignedNodeAndFrozenInputs(t *testing.T) {
	t.Parallel()
	handoff := testNodeActuation()
	request, err := ProjectRequest(handoff, nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "node-uid"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Handoff.UID != "handoff-uid" || request.Run.UID != "run-uid" ||
		request.Node.UID != "node-uid" || request.Target.JetsonNVPModel == nil ||
		request.Target.JetsonNVPModel.ID != 2 || request.PolicyDigest != handoff.Spec.PolicyDigest ||
		request.DesiredStage != nodeactuator.DesiredStageApply {
		t.Fatalf("projected request = %#v", request)
	}
}

func TestProjectRequestRejectsForeignNodeAndOwner(t *testing.T) {
	t.Parallel()
	handoff := testNodeActuation()
	if _, err := ProjectRequest(
		handoff,
		nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "other-node"},
	); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("foreign Node error = %v", err)
	}
	handoff.OwnerReferences[0].UID = "other-run"
	if _, err := ProjectRequest(
		handoff,
		nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "node-uid"},
	); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("foreign owner error = %v", err)
	}
}

func testNodeActuation() *edgev1alpha1.NodeActuation {
	requested := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "handoff", UID: "handoff-uid", Generation: 1,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "ProfilingRun",
				Name: "run", UID: types.UID("run-uid"), Controller: ptr.To(true),
			}},
		},
		Spec: edgev1alpha1.NodeActuationSpec{
			Run:         edgev1alpha1.ProfilingRunObjectReference{Name: "run", UID: "run-uid"},
			Node:        edgev1alpha1.ProfilingRunObjectReference{Name: "jetsonx", UID: "node-uid"},
			DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
			Actuator: edgev1alpha1.PowerModeActuator{
				Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
				JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 2},
			},
			PolicyDigest:            "sha256:" + strings.Repeat("a", 64),
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
			Deadline:         metav1.NewTime(requested.Add(15 * time.Minute)),
			DesiredStage:     edgev1alpha1.NodeActuationDesiredStageApply,
			StageRequestedAt: metav1.NewTime(requested),
		},
	}
}
