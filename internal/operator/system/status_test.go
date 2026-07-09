package system

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const testOperatorDesiredReplicas = int32(1)

var (
	testOperatorDeploymentName     = DefaultOperatorDeploymentName()
	testNodeDiscoveryDaemonSetName = DefaultNodeDiscoveryDaemonSetName()
)

func TestBuildStatusReadyWithDiscoveryDisabled(t *testing.T) {
	status := buildStatus(Observation{
		ObservedGeneration:     7,
		Namespace:              "chill-system",
		OperatorDeploymentName: testOperatorDeploymentName,
		OperatorDeployment:     deployment(1),
		NodeDiscoveryEnabled:   false,
		DeviceClassCount:       int32Ptr(3),
		ObservedNodeCount:      int32Ptr(6),
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseReady {
		t.Fatalf("Phase = %q, want Ready", status.Phase)
	}
	if status.Ready != metav1.ConditionTrue {
		t.Fatalf("Ready = %q, want True", status.Ready)
	}
	if status.OperatorState != edgev1alpha1.ComponentStateReady {
		t.Fatalf("OperatorState = %q, want Ready", status.OperatorState)
	}
	if status.NodeDiscoveryState != edgev1alpha1.ComponentStateDisabled {
		t.Fatalf("NodeDiscoveryState = %q, want Disabled", status.NodeDiscoveryState)
	}
	if status.DeviceClassCount == nil || *status.DeviceClassCount != 3 || status.ObservedNodeCount == nil || *status.ObservedNodeCount != 6 {
		t.Fatalf("counts = classes:%s nodes:%s, want classes:3 nodes:6", countString(status.DeviceClassCount), countString(status.ObservedNodeCount))
	}
	ready := findCondition(status.Conditions, edgev1alpha1.ChillSystemConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != 7 {
		t.Fatalf("Ready condition = %#v, want true observedGeneration=7", ready)
	}
}

func TestBuildStatusProgressingWhenNodeDiscoveryRollsOut(t *testing.T) {
	status := buildStatus(Observation{
		Namespace:                  "chill-system",
		OperatorDeploymentName:     testOperatorDeploymentName,
		OperatorDeployment:         deployment(1),
		NodeDiscoveryEnabled:       true,
		NodeDiscoveryDaemonSetName: testNodeDiscoveryDaemonSetName,
		NodeDiscoveryDaemonSet:     daemonSet(6, 4),
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseProgressing {
		t.Fatalf("Phase = %q, want Progressing", status.Phase)
	}
	if status.NodeDiscoveryState != edgev1alpha1.ComponentStateProgressing {
		t.Fatalf("NodeDiscoveryState = %q, want Progressing", status.NodeDiscoveryState)
	}
	progressing := findCondition(status.Conditions, edgev1alpha1.ChillSystemConditionProgressing)
	if progressing == nil || progressing.Status != metav1.ConditionTrue {
		t.Fatalf("Progressing condition = %#v, want true", progressing)
	}
}

func TestBuildStatusDegradedWhenRequiredComponentMissing(t *testing.T) {
	status := buildStatus(Observation{
		Namespace:                  "chill-system",
		OperatorDeploymentName:     testOperatorDeploymentName,
		OperatorDeployment:         deployment(1),
		NodeDiscoveryEnabled:       true,
		NodeDiscoveryDaemonSetName: testNodeDiscoveryDaemonSetName,
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseDegraded {
		t.Fatalf("Phase = %q, want Degraded", status.Phase)
	}
	if status.NodeDiscoveryState != edgev1alpha1.ComponentStateDegraded {
		t.Fatalf("NodeDiscoveryState = %q, want Degraded", status.NodeDiscoveryState)
	}
	degraded := findCondition(status.Conditions, edgev1alpha1.ChillSystemConditionDegraded)
	if degraded == nil || degraded.Status != metav1.ConditionTrue {
		t.Fatalf("Degraded condition = %#v, want true", degraded)
	}
}

func TestBuildStatusDegradedWhenDeploymentTimedOut(t *testing.T) {
	operator := deployment(0)
	operator.Status.Conditions = []appsv1.DeploymentCondition{
		{
			Type:    appsv1.DeploymentProgressing,
			Status:  corev1.ConditionFalse,
			Reason:  deploymentTimedOutReason,
			Message: "ReplicaSet has timed out progressing.",
		},
	}

	status := buildStatus(Observation{
		Namespace:              "chill-system",
		OperatorDeploymentName: testOperatorDeploymentName,
		OperatorDeployment:     operator,
		NodeDiscoveryEnabled:   false,
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseDegraded {
		t.Fatalf("Phase = %q, want Degraded", status.Phase)
	}
	if status.OperatorState != edgev1alpha1.ComponentStateDegraded {
		t.Fatalf("OperatorState = %q, want Degraded", status.OperatorState)
	}
	if status.Message != "ReplicaSet has timed out progressing." {
		t.Fatalf("Message = %q, want Deployment condition message", status.Message)
	}
}

func TestBuildStatusTruncatesExternalMessages(t *testing.T) {
	operator := deployment(0)
	longMessage := strings.Repeat("x", edgev1alpha1.ChillSystemMessageMaxLength+1)
	operator.Status.Conditions = []appsv1.DeploymentCondition{
		{
			Type:    appsv1.DeploymentProgressing,
			Status:  corev1.ConditionFalse,
			Reason:  deploymentTimedOutReason,
			Message: longMessage,
		},
	}

	status := buildStatus(Observation{
		Namespace:              "chill-system",
		OperatorDeploymentName: testOperatorDeploymentName,
		OperatorDeployment:     operator,
		NodeDiscoveryEnabled:   false,
	}, nil, metav1.Now())

	if len([]rune(status.Message)) > edgev1alpha1.ChillSystemMessageMaxLength {
		t.Fatalf("Message length = %d, want <= %d", len([]rune(status.Message)), edgev1alpha1.ChillSystemMessageMaxLength)
	}
	if len([]rune(status.Components[0].Message)) > edgev1alpha1.ChillSystemMessageMaxLength {
		t.Fatalf(
			"component message length = %d, want <= %d",
			len([]rune(status.Components[0].Message)),
			edgev1alpha1.ChillSystemMessageMaxLength,
		)
	}
}

func TestBuildStatusDegradedWhenDaemonSetHasReplicaFailure(t *testing.T) {
	nodeDiscovery := daemonSet(6, 4)
	nodeDiscovery.Status.Conditions = []appsv1.DaemonSetCondition{
		{
			Type:    daemonSetReplicaFailureConditionType,
			Status:  corev1.ConditionTrue,
			Reason:  "FailedCreate",
			Message: "Failed to create pod.",
		},
	}

	status := buildStatus(Observation{
		Namespace:                  "chill-system",
		OperatorDeploymentName:     testOperatorDeploymentName,
		OperatorDeployment:         deployment(1),
		NodeDiscoveryEnabled:       true,
		NodeDiscoveryDaemonSetName: testNodeDiscoveryDaemonSetName,
		NodeDiscoveryDaemonSet:     nodeDiscovery,
	}, nil, metav1.Now())

	if status.Phase != edgev1alpha1.ChillSystemPhaseDegraded {
		t.Fatalf("Phase = %q, want Degraded", status.Phase)
	}
	if status.NodeDiscoveryState != edgev1alpha1.ComponentStateDegraded {
		t.Fatalf("NodeDiscoveryState = %q, want Degraded", status.NodeDiscoveryState)
	}
	if status.Message != "Failed to create pod." {
		t.Fatalf("Message = %q, want DaemonSet condition message", status.Message)
	}
}

func TestBuildStatusDegradedOnObservationError(t *testing.T) {
	status := buildStatus(Observation{
		Namespace:              "chill-system",
		OperatorDeploymentName: testOperatorDeploymentName,
		OperatorDeployment:     deployment(1),
		NodeDiscoveryEnabled:   false,
		DeviceClassError:       errors.New("observe DeviceClasses: forbidden"),
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
		ObservedGeneration:     1,
		Namespace:              "chill-system",
		OperatorDeploymentName: testOperatorDeploymentName,
		OperatorDeployment:     deployment(1),
		NodeDiscoveryEnabled:   false,
	}, []metav1.Condition{
		{
			Type:               edgev1alpha1.ChillSystemConditionReady,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
			LastTransitionTime: firstTransition,
			Reason:             reasonReady,
			Message:            "CHILL operator is running; node-discovery is disabled",
		},
	}, metav1.Now())

	ready := findCondition(status.Conditions, edgev1alpha1.ChillSystemConditionReady)
	if ready == nil {
		t.Fatal("Ready condition not found")
	}
	if !ready.LastTransitionTime.Equal(&firstTransition) {
		t.Fatalf("LastTransitionTime = %s, want %s", ready.LastTransitionTime, firstTransition)
	}
}

func deployment(available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: testOperatorDeploymentName},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(testOperatorDesiredReplicas),
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:     available,
			AvailableReplicas: available,
		},
	}
}

func daemonSet(desired, ready int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: testNodeDiscoveryDaemonSetName},
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

func countString(count *int32) string {
	if count == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *count)
}
