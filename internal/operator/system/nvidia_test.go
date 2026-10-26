package system

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNvidiaOperandParticipatesInActuationReadiness(t *testing.T) {
	observed := Observation{
		NodeAgentActuation: DaemonSetObservation{Enabled: true, DaemonSet: namedDaemonSet("chill-node-agent-actuation", 1, 1)},
		NodeAgentNvidia:    DaemonSetObservation{Enabled: true, DaemonSet: namedDaemonSet("chill-node-agent-actuation-nvidia", 1, 0)},
	}
	status := buildStatus(observed, nil, metav1.Now())
	condition := requireCondition(t, status, edgev1alpha1.ChillSystemConditionNodeAgentActuationAvailable)
	if condition.Status == metav1.ConditionTrue {
		t.Fatal("unready NVIDIA operand reported ready")
	}
	observed.NodeAgentNvidia.DaemonSet = namedDaemonSet("chill-node-agent-actuation-nvidia", 1, 1)
	status = buildStatus(observed, nil, metav1.Now())
	condition = requireCondition(t, status, edgev1alpha1.ChillSystemConditionNodeAgentActuationAvailable)
	if condition.Status != metav1.ConditionTrue {
		t.Fatalf("ready operands reported %v", condition)
	}
	observed.NodeAgentActuation.Enabled = false
	status = buildStatus(observed, nil, metav1.Now())
	condition = requireCondition(t, status, edgev1alpha1.ChillSystemConditionNodeAgentActuationAvailable)
	if condition.Status != metav1.ConditionTrue {
		t.Fatal("NVIDIA-only installation requires a Jetson operand")
	}
}
