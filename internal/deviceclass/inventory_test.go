package deviceclass

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestProjectInventoryMatchesAndSortsLiveNodes(t *testing.T) {
	classes := []edgev1alpha1.DeviceClass{
		deviceClass("z-class", map[string]string{"class": "z"}),
		deviceClass("a-class", map[string]string{"class": "a"}),
	}
	nodes := []corev1.Node{
		node("z-node", map[string]string{"class": "a"}, corev1.ConditionFalse, true),
		node("ignored", map[string]string{"class": "other"}, corev1.ConditionTrue, false),
		node("a-node", map[string]string{"class": "a"}, corev1.ConditionTrue, false),
		nodeWithoutReady("unknown-node", map[string]string{"class": "z"}),
	}

	got, err := ProjectInventory(classes, nodes)
	if err != nil {
		t.Fatalf("ProjectInventory() error = %v", err)
	}
	if len(got) != 2 || got[0].Name != "a-class" || got[1].Name != "z-class" {
		t.Fatalf("class order = %#v", got)
	}

	aNodes := got[0].Nodes
	if len(aNodes) != 2 || aNodes[0].Name != "a-node" || aNodes[1].Name != "z-node" {
		t.Fatalf("a-class Nodes = %#v", aNodes)
	}
	if aNodes[0].Ready != corev1.ConditionTrue || aNodes[0].SchedulingDisabled {
		t.Fatalf("a-node observation = %#v", aNodes[0])
	}
	if aNodes[1].Ready != corev1.ConditionFalse || !aNodes[1].SchedulingDisabled {
		t.Fatalf("z-node observation = %#v", aNodes[1])
	}

	zNodes := got[1].Nodes
	if len(zNodes) != 1 || zNodes[0].Name != "unknown-node" || zNodes[0].Ready != corev1.ConditionUnknown {
		t.Fatalf("z-class Nodes = %#v", zNodes)
	}
}

func TestProjectInventoryPreservesOverlappingMembership(t *testing.T) {
	classes := []edgev1alpha1.DeviceClass{
		deviceClass("architecture", map[string]string{"kubernetes.io/arch": "arm64"}),
		deviceClass("accelerator", map[string]string{"accelerator": "npu"}),
	}
	nodes := []corev1.Node{
		node("edge", map[string]string{"kubernetes.io/arch": "arm64", "accelerator": "npu"}, corev1.ConditionTrue, false),
	}

	got, err := ProjectInventory(classes, nodes)
	if err != nil {
		t.Fatalf("ProjectInventory() error = %v", err)
	}
	if len(got) != 2 || len(got[0].Nodes) != 1 || len(got[1].Nodes) != 1 {
		t.Fatalf("overlapping inventory = %#v", got)
	}
}

func TestProjectInventoryRejectsMissingOrInvalidSelector(t *testing.T) {
	tests := []struct {
		name     string
		selector map[string]string
	}{
		{name: "missing"},
		{name: "invalid", selector: map[string]string{"bad key": "value"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ProjectInventory([]edgev1alpha1.DeviceClass{deviceClass("class", tt.selector)}, nil)
			if err == nil {
				t.Fatal("ProjectInventory() error = nil")
			}
		})
	}
}

func TestProjectInventoryCopiesSelectorAndSourceIdentity(t *testing.T) {
	class := deviceClass("class", map[string]string{"class": "edge"})
	class.Spec.PowerModes = []edgev1alpha1.PowerMode{{Name: "7W", Actuator: edgev1alpha1.PowerModeActuator{
		Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
		JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 1},
	}}}
	class.UID = types.UID("class-uid")
	class.ResourceVersion = "12"
	n := node("node", map[string]string{"class": "edge"}, corev1.ConditionTrue, false)
	n.UID = types.UID("node-uid")
	n.ResourceVersion = "34"

	got, err := ProjectInventory([]edgev1alpha1.DeviceClass{class}, []corev1.Node{n})
	if err != nil {
		t.Fatalf("ProjectInventory() error = %v", err)
	}
	got[0].Spec.NodeSelector["class"] = "changed"
	got[0].Spec.PowerModes[0].Actuator.JetsonNVPModel.ID = 2
	if class.Spec.NodeSelector["class"] != "edge" || class.Spec.PowerModes[0].Actuator.JetsonNVPModel.ID != 1 {
		t.Fatalf("source spec mutated: %#v", class.Spec)
	}
	if got[0].UID != "class-uid" || got[0].ResourceVersion != "12" ||
		got[0].Nodes[0].UID != "node-uid" || got[0].Nodes[0].ResourceVersion != "34" {
		t.Fatalf("source identity = %#v", got[0])
	}
}

func deviceClass(name string, selector map[string]string) edgev1alpha1.DeviceClass {
	return edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: edgev1alpha1.DeviceClassSpec{
			NodeSelector: selector,
			Architecture: "arm64",
		},
	}
}

func node(
	name string,
	labels map[string]string,
	ready corev1.ConditionStatus,
	unschedulable bool,
) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: ready,
		}}},
	}
}

func nodeWithoutReady(name string, labels map[string]string) corev1.Node {
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}
