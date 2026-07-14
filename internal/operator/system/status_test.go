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
var testProfilerDeploymentName = component.ProfilerDeploymentName(component.DefaultSystemName)

func TestBuildStatusReadyWithAllComponentsDisabled(t *testing.T) {
	status := buildStatus(Observation{ObservedGeneration: 7}, nil, metav1.Now())
	ready := requireCondition(t, status, edgev1alpha1.ChillSystemConditionReady)
	if ready.Status != metav1.ConditionTrue || ready.Reason != reasonComponentsDisabled {
		t.Fatalf("Ready condition = %#v, want True/%s", ready, reasonComponentsDisabled)
	}
	if len(status.Conditions) != 1 {
		t.Fatalf("Conditions = %#v, want only Ready", status.Conditions)
	}
	if ready.ObservedGeneration != 7 || status.ObservedGeneration != 7 {
		t.Fatalf("observed generations = condition:%d status:%d, want 7", ready.ObservedGeneration, status.ObservedGeneration)
	}
}

func TestBuildStatusReadyWhenEnabledComponentsAreAvailable(t *testing.T) {
	status := buildStatus(Observation{
		NodeDiscoveryEnabled:   true,
		NodeDiscoveryDaemonSet: daemonSet(6, 6),
		ProfilerEnabled:        true,
		ProfilerDeployment:     deployment(1, 1),
	}, nil, metav1.Now())

	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionNodeDiscoveryAvailable, metav1.ConditionTrue, reasonNodeDiscoveryReady)
	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionProfilerAvailable, metav1.ConditionTrue, reasonProfilerReady)
	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionReady, metav1.ConditionTrue, reasonComponentsAvailable)
}

func TestBuildStatusNotReadyWhenProfilerIsMissing(t *testing.T) {
	status := buildStatus(Observation{
		NodeDiscoveryEnabled:   true,
		NodeDiscoveryDaemonSet: daemonSet(6, 6),
		ProfilerEnabled:        true,
		ProfilerDeploymentName: testProfilerDeploymentName,
	}, nil, metav1.Now())

	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionNodeDiscoveryAvailable, metav1.ConditionTrue, reasonNodeDiscoveryReady)
	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionProfilerAvailable, metav1.ConditionFalse, reasonProfilerMissing)
	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionReady, metav1.ConditionFalse, reasonComponentsNotReady)
}

func TestBuildStatusNotReadyWhileNodeDiscoveryProgresses(t *testing.T) {
	status := buildStatus(Observation{
		NodeDiscoveryEnabled:   true,
		NodeDiscoveryDaemonSet: daemonSet(6, 4),
	}, nil, metav1.Now())

	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionNodeDiscoveryAvailable, metav1.ConditionFalse, reasonNodeDiscoveryProgressing)
	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionReady, metav1.ConditionFalse, reasonComponentsNotReady)
}

func TestBuildStatusUnknownOnObservationFailure(t *testing.T) {
	status := buildStatus(Observation{
		ProfilerEnabled: true,
		ProfilerError:   errors.New("forbidden"),
	}, nil, metav1.Now())

	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionProfilerAvailable, metav1.ConditionUnknown, reasonObservationFailed)
	requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionReady, metav1.ConditionUnknown, reasonObservationFailed)
}

func TestBuildStatusNotReadyOnNativeWorkloadFailure(t *testing.T) {
	daemonSet := daemonSet(6, 4)
	daemonSet.Status.Conditions = []appsv1.DaemonSetCondition{{
		Type: daemonSetReplicaFailureConditionType, Status: corev1.ConditionTrue, Message: "daemonset failed",
	}}
	profiler := deployment(1, 0)
	profiler.Status.Conditions = []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Message: "deployment failed",
	}}
	status := buildStatus(Observation{
		NodeDiscoveryEnabled:   true,
		NodeDiscoveryDaemonSet: daemonSet,
		ProfilerEnabled:        true,
		ProfilerDeployment:     profiler,
	}, nil, metav1.Now())

	nodeCondition := requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionNodeDiscoveryAvailable, metav1.ConditionFalse, reasonNodeDiscoveryDegraded)
	if nodeCondition.Message != "daemonset failed" {
		t.Fatalf("NodeDiscoveryAvailable message = %q", nodeCondition.Message)
	}
	profilerCondition := requireConditionStatus(t, status, edgev1alpha1.ChillSystemConditionProfilerAvailable, metav1.ConditionFalse, reasonProfilerDegraded)
	if profilerCondition.Message != "deployment failed" {
		t.Fatalf("ProfilerAvailable message = %q", profilerCondition.Message)
	}
}

func TestBuildStatusTruncatesExternalMessages(t *testing.T) {
	longMessage := strings.Repeat("x", edgev1alpha1.ChillSystemMessageMaxLength+1)
	status := buildStatus(Observation{ProfilerEnabled: true, ProfilerError: errors.New(longMessage)}, nil, metav1.Now())
	condition := requireCondition(t, status, edgev1alpha1.ChillSystemConditionProfilerAvailable)
	if len([]rune(condition.Message)) > edgev1alpha1.ChillSystemMessageMaxLength {
		t.Fatalf("message length = %d", len([]rune(condition.Message)))
	}
}

func TestBuildStatusPreservesTransitionTimeWithoutStatusChange(t *testing.T) {
	firstTransition := metav1.Now()
	status := buildStatus(Observation{ObservedGeneration: 2}, []metav1.Condition{{
		Type:               edgev1alpha1.ChillSystemConditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: 1,
		LastTransitionTime: firstTransition,
		Reason:             reasonComponentsDisabled,
		Message:            "all managed components are disabled",
	}}, metav1.Now())

	ready := requireCondition(t, status, edgev1alpha1.ChillSystemConditionReady)
	if !ready.LastTransitionTime.Equal(&firstTransition) {
		t.Fatalf("LastTransitionTime = %s, want %s", ready.LastTransitionTime, firstTransition)
	}
	if ready.ObservedGeneration != 2 {
		t.Fatalf("ObservedGeneration = %d, want 2", ready.ObservedGeneration)
	}
}

func TestBuildStatusDropsDisabledAndObsoleteConditions(t *testing.T) {
	status := buildStatus(Observation{}, []metav1.Condition{
		{Type: edgev1alpha1.ChillSystemConditionReady, Status: metav1.ConditionTrue},
		{Type: edgev1alpha1.ChillSystemConditionProfilerAvailable, Status: metav1.ConditionTrue},
		{Type: "Progressing", Status: metav1.ConditionFalse},
	}, metav1.Now())
	if len(status.Conditions) != 1 || status.Conditions[0].Type != edgev1alpha1.ChillSystemConditionReady {
		t.Fatalf("Conditions = %#v, want only Ready", status.Conditions)
	}
}

func daemonSet(desired, ready int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: testNodeDiscoveryDaemonSetName},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: desired, NumberReady: ready, NumberAvailable: ready},
	}
}

func deployment(desired, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: testProfilerDeploymentName, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: available},
	}
}

func requireConditionStatus(t *testing.T, status edgev1alpha1.ChillSystemStatus, conditionType string, wantStatus metav1.ConditionStatus, wantReason string) *metav1.Condition {
	t.Helper()
	condition := requireCondition(t, status, conditionType)
	if condition.Status != wantStatus || condition.Reason != wantReason {
		t.Fatalf("%s condition = %#v, want %s/%s", conditionType, condition, wantStatus, wantReason)
	}
	return condition
}

func requireCondition(t *testing.T, status edgev1alpha1.ChillSystemStatus, conditionType string) *metav1.Condition {
	t.Helper()
	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			return &status.Conditions[i]
		}
	}
	t.Fatalf("%s condition not found", conditionType)
	return nil
}
