package system

import (
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
)

var testNodeDiscoveryDaemonSetName = component.NodeDiscoveryDaemonSetName(component.DefaultSystemName)

func TestBuildStatusReadyWithDiscoveryDisabled(t *testing.T) {
	status := buildStatus(Observation{
		ObservedGeneration:   7,
		NodeDiscoveryEnabled: false,
	}, nil, metav1.Now())

	ready := requireReadyCondition(t, status)
	if ready.Status != metav1.ConditionTrue || ready.Reason != reasonNodeDiscoveryDisabled {
		t.Fatalf("Ready condition = %#v, want True/%s", ready, reasonNodeDiscoveryDisabled)
	}
	if ready.ObservedGeneration != 7 || status.ObservedGeneration != 7 {
		t.Fatalf("observed generations = condition:%d status:%d, want 7", ready.ObservedGeneration, status.ObservedGeneration)
	}
}

func TestBuildStatusReadyWhenNodeDiscoveryIsReady(t *testing.T) {
	status := buildStatus(Observation{
		NodeDiscoveryEnabled:   true,
		NodeDiscoveryDaemonSet: daemonSet(6, 6),
	}, nil, metav1.Now())

	ready := requireReadyCondition(t, status)
	if ready.Status != metav1.ConditionTrue || ready.Reason != reasonNodeDiscoveryReady {
		t.Fatalf("Ready condition = %#v, want True/%s", ready, reasonNodeDiscoveryReady)
	}
}

func TestBuildStatusNotReadyWhileNodeDiscoveryRollsOut(t *testing.T) {
	status := buildStatus(Observation{
		NodeDiscoveryEnabled:   true,
		NodeDiscoveryDaemonSet: daemonSet(6, 4),
	}, nil, metav1.Now())

	ready := requireReadyCondition(t, status)
	if ready.Status != metav1.ConditionFalse || ready.Reason != reasonNodeDiscoveryProgressing {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, reasonNodeDiscoveryProgressing)
	}
	if !strings.Contains(ready.Message, "4/6") {
		t.Fatalf("Ready message = %q, want replica progress", ready.Message)
	}
}

func TestBuildStatusNotReadyWhenNodeDiscoveryIsMissing(t *testing.T) {
	status := buildStatus(Observation{
		NodeDiscoveryEnabled:       true,
		NodeDiscoveryDaemonSetName: testNodeDiscoveryDaemonSetName,
	}, nil, metav1.Now())

	ready := requireReadyCondition(t, status)
	if ready.Status != metav1.ConditionFalse || ready.Reason != reasonNodeDiscoveryMissing {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, reasonNodeDiscoveryMissing)
	}
}

func TestBuildStatusUnknownOnObservationFailure(t *testing.T) {
	status := buildStatus(Observation{
		NodeDiscoveryEnabled: true,
		NodeDiscoveryError:   errors.New("forbidden"),
	}, nil, metav1.Now())

	ready := requireReadyCondition(t, status)
	if ready.Status != metav1.ConditionUnknown || ready.Reason != reasonObservationFailed {
		t.Fatalf("Ready condition = %#v, want Unknown/%s", ready, reasonObservationFailed)
	}
}

func TestBuildStatusNotReadyOnDaemonSetFailure(t *testing.T) {
	daemonSet := daemonSet(6, 4)
	daemonSet.Status.Conditions = []appsv1.DaemonSetCondition{
		{
			Type:    daemonSetReplicaFailureConditionType,
			Status:  corev1.ConditionTrue,
			Reason:  "FailedCreate",
			Message: "Failed to create Pod.",
		},
	}

	status := buildStatus(Observation{
		NodeDiscoveryEnabled:   true,
		NodeDiscoveryDaemonSet: daemonSet,
	}, nil, metav1.Now())

	ready := requireReadyCondition(t, status)
	if ready.Status != metav1.ConditionFalse || ready.Reason != reasonNodeDiscoveryDegraded {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, reasonNodeDiscoveryDegraded)
	}
	if ready.Message != "Failed to create Pod." {
		t.Fatalf("Ready message = %q, want DaemonSet failure message", ready.Message)
	}
}

func TestBuildStatusTruncatesExternalMessages(t *testing.T) {
	longMessage := strings.Repeat("x", edgev1alpha1.ChillSystemMessageMaxLength+1)
	status := buildStatus(Observation{
		NodeDiscoveryEnabled: true,
		NodeDiscoveryError:   errors.New(longMessage),
	}, nil, metav1.Now())

	ready := requireReadyCondition(t, status)
	if len([]rune(ready.Message)) > edgev1alpha1.ChillSystemMessageMaxLength {
		t.Fatalf("Ready message length = %d, want <= %d", len([]rune(ready.Message)), edgev1alpha1.ChillSystemMessageMaxLength)
	}
}

func TestBuildStatusPreservesTransitionTimeWithoutStatusChange(t *testing.T) {
	firstTransition := metav1.Now()
	status := buildStatus(Observation{
		ObservedGeneration:   2,
		NodeDiscoveryEnabled: false,
	}, []metav1.Condition{
		{
			Type:               edgev1alpha1.ChillSystemConditionReady,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
			LastTransitionTime: firstTransition,
			Reason:             reasonNodeDiscoveryDisabled,
			Message:            "node-discovery is disabled",
		},
	}, metav1.Now())

	ready := requireReadyCondition(t, status)
	if !ready.LastTransitionTime.Equal(&firstTransition) {
		t.Fatalf("LastTransitionTime = %s, want %s", ready.LastTransitionTime, firstTransition)
	}
	if ready.ObservedGeneration != 2 {
		t.Fatalf("ObservedGeneration = %d, want 2", ready.ObservedGeneration)
	}
}

func TestBuildStatusDropsObsoleteConditions(t *testing.T) {
	status := buildStatus(Observation{
		NodeDiscoveryEnabled: false,
	}, []metav1.Condition{
		{Type: edgev1alpha1.ChillSystemConditionReady, Status: metav1.ConditionTrue, Reason: "Ready"},
		{Type: "Progressing", Status: metav1.ConditionFalse, Reason: "Ready"},
		{Type: "Degraded", Status: metav1.ConditionFalse, Reason: "Ready"},
	}, metav1.Now())

	if len(status.Conditions) != 1 || status.Conditions[0].Type != edgev1alpha1.ChillSystemConditionReady {
		t.Fatalf("Conditions = %#v, want only Ready", status.Conditions)
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

func requireReadyCondition(t *testing.T, status edgev1alpha1.ChillSystemStatus) *metav1.Condition {
	t.Helper()
	for i := range status.Conditions {
		if status.Conditions[i].Type == edgev1alpha1.ChillSystemConditionReady {
			return &status.Conditions[i]
		}
	}
	t.Fatal("Ready condition not found")
	return nil
}
