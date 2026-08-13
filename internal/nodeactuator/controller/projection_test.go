package controller

import (
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestProjectRequestBindsExactAssignedNodeAndFrozenInputs(t *testing.T) {
	t.Parallel()
	handoff := testNodeActuation()
	request, err := ProjectRequest(handoff, nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "node-uid"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Handoff.UID != "handoff-uid" || request.Node.UID != "node-uid" ||
		request.Target.JetsonNVPModel == nil ||
		request.Target.JetsonNVPModel.ID != 2 || request.PolicyName != handoff.Spec.PolicyName {
		t.Fatalf("projected request = %#v", request)
	}
}

func TestProjectRequestRejectsForeignNode(t *testing.T) {
	t.Parallel()
	handoff := testNodeActuation()
	if _, err := ProjectRequest(
		handoff,
		nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "other-node"},
	); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("foreign Node error = %v", err)
	}
}

func testNodeActuation() *edgev1alpha1.NodeActuation {
	requested := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "handoff", UID: "handoff-uid", Generation: 1,
		},
		Spec: edgev1alpha1.NodeActuationSpec{
			Node:        edgev1alpha1.ProfilingRunObjectReference{Name: "jetsonx", UID: "node-uid"},
			DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
			Actuator: edgev1alpha1.PowerModeActuator{
				Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
				JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 2},
			},
			PolicyName:              "actuation-policy-v1",
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
			Deadline:         metav1.NewTime(requested.Add(15 * time.Minute)),
			StageRequestedAt: metav1.NewTime(requested),
		},
	}
}
