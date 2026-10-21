package controller

import (
	"context"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/component"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/metadata"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
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

func TestObserveActuationRecordsNodeApplyAndGeneration(t *testing.T) {
	t.Parallel()
	run, resolved := testActuatedRunAndResolved(t)
	config := resolved.Prepared.RunConfig
	readyAt := time.Date(2026, 8, 6, 12, 0, 30, 0, time.UTC)
	node := settledNode(config, config.Actuation.PowerMode, readyAt, 5)
	facts, err := observeActuation(run, resolved.Prepared, node)
	if err != nil {
		t.Fatal(err)
	}
	if facts == nil || facts.Run.UID != string(run.UID) || facts.Handoff != nil ||
		facts.Target.PowerMode != config.Actuation.PowerMode || !facts.ReadyAt.Equal(readyAt) ||
		!facts.Target.ObservedAt.Equal(readyAt.Add(-30*time.Second)) ||
		facts.Generation == nil || *facts.Generation != (profilerevidence.PowerModeGeneration{Start: 5, End: 5}) {
		t.Fatalf("actuation facts = %#v", facts)
	}

	node.Annotations[metadata.PowerModeGeneration] = "6"
	if facts, err := observeActuation(run, resolved.Prepared, node); err != nil ||
		*facts.Generation != (profilerevidence.PowerModeGeneration{Start: 5, End: 6}) {
		t.Fatalf("host change facts = %#v, %v", facts, err)
	}

	delete(node.Annotations, metadata.PowerModeTransition)
	if _, err := observeActuation(run, resolved.Prepared, node); err == nil {
		t.Fatal("Node without a settled apply was accepted for finalization")
	}
}

func unusedStoreFactory(EvidenceStoreBinding, string, string) (profilerevidence.ObjectStore, error) {
	return nil, nil
}

func TestFinalizedEvidenceObjectKeyIsRunScoped(t *testing.T) {
	t.Parallel()
	want := "profiling-runs/run-uid/execution.json"
	if got := finalizedEvidenceObjectKey(types.UID("run-uid")); got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}
