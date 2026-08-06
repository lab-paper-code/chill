package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesExecutionFinalizerObservesExactJobPodAndNodeWorkload(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{batchv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace: "chill-system", Name: "run-job", UID: "job-uid",
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "run-pod", UID: "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{NodeName: "lattepanda"},
	}
	coResident := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "edge-metric", UID: "metric-uid"},
		Spec:       corev1.PodSpec{NodeName: "lattepanda"},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "lattepanda", UID: "node-uid"}}
	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string {
			return []string{object.(*corev1.Pod).Spec.NodeName}
		}).
		WithObjects(job, pod, coResident, node).
		Build()
	finalizer, err := NewKubernetesExecutionFinalizer(reader, unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	finalizer.now = func() time.Time { return now }
	run := testRun()
	observation, err := finalizer.observe(context.Background(), run, job)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Pod.UID != pod.UID || observation.Node.UID != node.UID ||
		len(observation.NodePods) != 2 || !observation.ObservedAt.Equal(now) {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestKubernetesExecutionFinalizerLoadsOnlyRequiredCredentials(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "store-credentials"},
		Data: map[string][]byte{
			component.EvidenceStoreAccessKeyKey: []byte("access"),
			component.EvidenceStoreSecretKeyKey: []byte("secret"),
			"unrelated":                         []byte("ignored"),
		},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	finalizer, err := NewKubernetesExecutionFinalizer(reader, unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := storekube.LoadCredentials(context.Background(), finalizer.reader, "chill-system", secret.Name)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessKeyID != "access" || credentials.SecretAccessKey != "secret" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestKubernetesExecutionFinalizerProjectsOnlyRestoredOwnedActuation(t *testing.T) {
	t.Parallel()
	run, frozen := testActuatedRunAndFrozen(t)
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	handoff, err := buildActuationHandoff(run, frozen, scheme, base)
	if err != nil {
		t.Fatal(err)
	}
	handoff.UID = "actuation-uid"
	handoff.Generation = 2
	handoff.Spec.DesiredStage = edgev1alpha1.NodeActuationDesiredStageRestore
	handoff.Spec.StageRequestedAt = metav1.NewTime(base.Add(time.Minute))
	observation := func(at time.Time) *edgev1alpha1.NodeActuationObservation {
		return &edgev1alpha1.NodeActuationObservation{
			PowerMode: handoff.Spec.PowerMode, Actuator: handoff.Spec.Actuator, ObservedAt: metav1.NewTime(at),
		}
	}
	readyAt := metav1.NewTime(base.Add(30 * time.Second))
	restoredAt := metav1.NewTime(base.Add(70 * time.Second))
	handoff.Status = edgev1alpha1.NodeActuationStatus{
		ObservedGeneration:  handoff.Generation,
		Phase:               edgev1alpha1.NodeActuationPhaseRestored,
		PreviousObservation: observation(base),
		TargetObservation:   observation(base.Add(20 * time.Second)),
		RestoredObservation: observation(base.Add(65 * time.Second)),
		ReadyAt:             &readyAt,
		RestoredAt:          &restoredAt,
	}
	run.Status.Actuation = &edgev1alpha1.ProfilingRunObjectReference{
		Name: handoff.Name, UID: string(handoff.UID),
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, handoff).Build()
	finalizer, err := NewKubernetesExecutionFinalizer(reader, unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := finalizer.observeActuation(context.Background(), run, frozen.Prepared)
	if err != nil {
		t.Fatal(err)
	}
	if facts == nil || facts.Run.UID != string(run.UID) || facts.Handoff.UID != string(handoff.UID) ||
		facts.Target.PowerMode != handoff.Spec.PowerMode || !facts.RestoredAt.Equal(restoredAt.Time) {
		t.Fatalf("actuation facts = %#v", facts)
	}

	drifted := handoff.DeepCopy()
	drifted.Status.Phase = edgev1alpha1.NodeActuationPhaseReady
	driftReader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, drifted).Build()
	driftFinalizer, err := NewKubernetesExecutionFinalizer(driftReader, unusedStoreFactory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driftFinalizer.observeActuation(context.Background(), run, frozen.Prepared); err == nil {
		t.Fatal("non-restored handoff was accepted for finalization")
	}
}

func unusedStoreFactory(EvidenceStoreBinding, string, string) (profilerevidence.ObjectStore, error) {
	return nil, nil
}

func TestFinalizedEvidenceObjectKeyIsRunAndIntentScoped(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("a", 64)
	want := "profiling-runs/run-uid/execution/" + strings.Repeat("a", 64) + ".json"
	if got := finalizedEvidenceObjectKey(types.UID("run-uid"), digest); got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}
