package controller

import (
	"context"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReleaseUsesOnlyEvaluationWriters(t *testing.T) {
	parent := fixtureRun("external-state")
	startRun(parent)
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionPoolAcquired, metav1.ConditionTrue, "Acquired", "")
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionCleanupComplete, metav1.ConditionFalse, api.EvaluationReasonReturnTimeout, "")
	foreign := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "autonomous-serving", Namespace: parent.Namespace}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	r, _ := fixtureRuntime(t, parent, foreign)
	annotation := `{"reason":"evaluation controls reverted and writers stopped","at":"2026-11-01T12:00:00Z"}`
	if _, err := (&RunReconciler{Runtime: r}).release(context.Background(), parent, annotation); err != nil {
		t.Fatal(err)
	}
	if occupied(parent) || isTrue(parent.Status.Conditions, api.EvaluationConditionPoolAcquired) || len(parent.Status.ReleaseRecords) != 1 {
		t.Fatal("external serving Pod prevented release")
	}
	if isTrue(parent.Status.Conditions, api.EvaluationConditionCleanupComplete) {
		t.Fatal("release erased cleanup failure")
	}
}

func TestInjectionUsesReadyExternalPodUID(t *testing.T) {
	r, _, parent, stage := fixtureStageExecution(t)
	evidence := &injectionEvidence{}
	if err := r.selectInjection(context.Background(), parent, stage, stage.Spec.FailureInjection[0], evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Record.PodRef == nil || evidence.Record.PodRef.UID != "pod-uid" || len(evidence.Candidates) != 1 || len(evidence.Pods) != 1 || len(evidence.Nodes) != 1 {
		t.Fatalf("external target evidence lost: %+v", evidence)
	}
	pod := &corev1.Pod{}
	if err := r.Client.Get(context.Background(), client.ObjectKey{Namespace: parent.Namespace, Name: "serving"}, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.Conditions[0].Status = corev1.ConditionFalse
	if err := r.Client.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if err := r.selectInjection(context.Background(), parent, stage, stage.Spec.FailureInjection[0], &injectionEvidence{}); err == nil {
		t.Fatal("unready Pod was selected")
	}
}
