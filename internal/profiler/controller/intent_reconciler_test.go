package controller

import (
	"context"
	"errors"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type recordingIntentFreezer struct {
	calls int
	run   *edgev1alpha1.ProfilingRun
	err   error
}

func (f *recordingIntentFreezer) Freeze(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
) (FreezeIntentResult, error) {
	f.calls++
	f.run = run.DeepCopy()
	return FreezeIntentResult{}, f.err
}

func TestIntentReconcilerFreezesExistingRun(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &edgev1alpha1.ProfilingRun{ObjectMeta: metav1.ObjectMeta{
		Namespace: "chill-system", Name: "cpu-run",
	}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	freezer := &recordingIntentFreezer{}
	reconciler, err := NewIntentReconciler(kubeClient, freezer)
	if err != nil {
		t.Fatal(err)
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: run.Namespace, Name: run.Name,
	}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if freezer.calls != 1 || freezer.run == nil || freezer.run.Name != run.Name {
		t.Fatalf("unexpected freezer call: calls=%d run=%#v", freezer.calls, freezer.run)
	}
}

func TestIntentReconcilerIgnoresMissingRun(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	freezer := &recordingIntentFreezer{}
	reconciler, err := NewIntentReconciler(kubeClient, freezer)
	if err != nil {
		t.Fatal(err)
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: "chill-system", Name: "missing",
	}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if freezer.calls != 0 {
		t.Fatalf("freezer called %d times for a missing Run", freezer.calls)
	}
}

func TestIntentReconcilerReturnsFreezeError(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &edgev1alpha1.ProfilingRun{ObjectMeta: metav1.ObjectMeta{
		Namespace: "chill-system", Name: "invalid-run",
	}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	want := errors.New("unresolved execution facts")
	reconciler, err := NewIntentReconciler(kubeClient, &recordingIntentFreezer{err: want})
	if err != nil {
		t.Fatal(err)
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: run.Namespace, Name: run.Name,
	}}
	if _, err := reconciler.Reconcile(context.Background(), request); !errors.Is(err, want) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, want)
	}
}
