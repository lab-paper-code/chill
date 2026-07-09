package systemcontroller

import (
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestBuildStatusReadyWithDiscoveryDisabled(t *testing.T) {
	status := buildStatus(Observation{
		ObservedGeneration:       7,
		Namespace:                "chill-system",
		ControllerDeploymentName: "chill-controller-manager",
		ControllerDeployment:     deployment("chill-controller-manager", 1, 1),
		NodeDiscoveryEnabled:     false,
		DeviceClassCount:         3,
		ObservedNodeCount:        6,
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseReady {
		t.Fatalf("Phase = %q, want Ready", status.Phase)
	}
	if status.Ready != metav1.ConditionTrue {
		t.Fatalf("Ready = %q, want True", status.Ready)
	}
	if status.ControllerState != edgev1alpha1.ComponentStateReady {
		t.Fatalf("ControllerState = %q, want Ready", status.ControllerState)
	}
	if status.NodeDiscoveryState != edgev1alpha1.ComponentStateDisabled {
		t.Fatalf("NodeDiscoveryState = %q, want Disabled", status.NodeDiscoveryState)
	}
	if status.DeviceClassCount != 3 || status.ObservedNodeCount != 6 {
		t.Fatalf("counts = classes:%d nodes:%d, want classes:3 nodes:6", status.DeviceClassCount, status.ObservedNodeCount)
	}
	ready := findCondition(status.Conditions, ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != 7 {
		t.Fatalf("Ready condition = %#v, want true observedGeneration=7", ready)
	}
}

func TestBuildStatusProgressingWhenNodeDiscoveryRollsOut(t *testing.T) {
	status := buildStatus(Observation{
		Namespace:                  "chill-system",
		ControllerDeploymentName:   "chill-controller-manager",
		ControllerDeployment:       deployment("chill-controller-manager", 1, 1),
		NodeDiscoveryEnabled:       true,
		NodeDiscoveryDaemonSetName: "chill-node-discovery",
		NodeDiscoveryDaemonSet:     daemonSet("chill-node-discovery", 6, 4),
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseProgressing {
		t.Fatalf("Phase = %q, want Progressing", status.Phase)
	}
	if status.NodeDiscoveryState != edgev1alpha1.ComponentStateProgressing {
		t.Fatalf("NodeDiscoveryState = %q, want Progressing", status.NodeDiscoveryState)
	}
	progressing := findCondition(status.Conditions, ConditionProgressing)
	if progressing == nil || progressing.Status != metav1.ConditionTrue {
		t.Fatalf("Progressing condition = %#v, want true", progressing)
	}
}

func TestBuildStatusDegradedWhenRequiredComponentMissing(t *testing.T) {
	status := buildStatus(Observation{
		Namespace:                  "chill-system",
		ControllerDeploymentName:   "chill-controller-manager",
		ControllerDeployment:       deployment("chill-controller-manager", 1, 1),
		NodeDiscoveryEnabled:       true,
		NodeDiscoveryDaemonSetName: "chill-node-discovery",
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseDegraded {
		t.Fatalf("Phase = %q, want Degraded", status.Phase)
	}
	if status.NodeDiscoveryState != edgev1alpha1.ComponentStateDegraded {
		t.Fatalf("NodeDiscoveryState = %q, want Degraded", status.NodeDiscoveryState)
	}
	degraded := findCondition(status.Conditions, ConditionDegraded)
	if degraded == nil || degraded.Status != metav1.ConditionTrue {
		t.Fatalf("Degraded condition = %#v, want true", degraded)
	}
}

func TestBuildStatusDegradedOnObservationError(t *testing.T) {
	status := buildStatus(Observation{
		Namespace:                "chill-system",
		ControllerDeploymentName: "chill-controller-manager",
		ControllerDeployment:     deployment("chill-controller-manager", 1, 1),
		NodeDiscoveryEnabled:     false,
		DeviceClassError:         errors.New("observe DeviceClasses: forbidden"),
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseDegraded {
		t.Fatalf("Phase = %q, want Degraded", status.Phase)
	}
	if status.Message != "observe DeviceClasses: forbidden" {
		t.Fatalf("Message = %q, want observation error", status.Message)
	}
}

func TestBuildStatusPreservesTransitionTimeWithoutStateChange(t *testing.T) {
	firstTransition := metav1.Now()
	status := buildStatus(Observation{
		ObservedGeneration:       1,
		Namespace:                "chill-system",
		ControllerDeploymentName: "chill-controller-manager",
		ControllerDeployment:     deployment("chill-controller-manager", 1, 1),
		NodeDiscoveryEnabled:     false,
	}, []metav1.Condition{
		{
			Type:               ConditionReady,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
			LastTransitionTime: firstTransition,
			Reason:             reasonReady,
			Message:            "CHILL controller is running; node-discovery is disabled",
		},
	}, metav1.Now())

	ready := findCondition(status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("Ready condition not found")
	}
	if !ready.LastTransitionTime.Equal(&firstTransition) {
		t.Fatalf("LastTransitionTime = %s, want %s", ready.LastTransitionTime, firstTransition)
	}
}

func deployment(name string, desired, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &desired,
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:     available,
			AvailableReplicas: available,
		},
	}
}

func daemonSet(name string, desired, ready int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: desired,
			NumberReady:            ready,
			NumberAvailable:        ready,
		},
	}
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
