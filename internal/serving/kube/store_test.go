package kube

import (
	"context"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStoreFinalizerAndDeletionConfirmation(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&edgev1alpha1.Workload{}).Build()}
	obj := &edgev1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "detector-fast", UID: types.UID("registration-1")},
		Spec: edgev1alpha1.WorkloadSpec{
			ModelRef:        edgev1alpha1.CatalogReference{Name: "detector"},
			PreprocessorRef: edgev1alpha1.CatalogReference{Name: "imagenet"},
			SLO:             edgev1alpha1.WorkloadSLO{Percentile: 99, TargetLatency: metav1.Duration{Duration: time.Second}},
		},
	}
	if err := store.CreateWorkload(ctx, obj); err != nil {
		t.Fatal(err)
	}
	if len(obj.Finalizers) != 1 || obj.Finalizers[0] != WorkloadFinalizer {
		t.Fatal("create did not persist cleanup finalizer")
	}
	if err := store.RemoveWorkloadFinalizer(ctx, obj); err == nil {
		t.Fatal("removed finalizer before deletion intent")
	}
	before, err := store.GetWorkload(ctx, obj.Name)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateWorkloadStatus(ctx, before, edgev1alpha1.WorkloadStatus{Admission: edgev1alpha1.WorkloadAdmission{Allowed: false, Reason: "Deleting", ObservedAt: metav1.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status.Admission.Reason != "Deleting" || updated.Spec.ModelRef.Name != "detector" {
		t.Fatal("status patch changed desired definition or lost status")
	}
	withoutVersion := updated.DeepCopy()
	withoutVersion.ResourceVersion = ""
	if err := store.DeleteWorkload(ctx, withoutVersion); err == nil {
		t.Fatal("delete accepted missing resourceVersion")
	}
	if _, err := store.UpdateWorkloadStatus(ctx, before, updated.Status); err == nil {
		t.Fatal("status patch accepted stale resourceVersion")
	}
	if err := store.DeleteWorkload(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.WorkloadDeleted(ctx, obj.Name, obj.UID); err != nil || deleted {
		t.Fatalf("deletion before finalizer removal = %v, %v", deleted, err)
	}
	deleting, err := store.GetWorkload(ctx, obj.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveWorkloadFinalizer(ctx, deleting); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.WorkloadDeleted(ctx, obj.Name, obj.UID); err != nil || !deleted {
		t.Fatalf("deletion after finalizer removal = %v, %v", deleted, err)
	}
	replacement := obj.DeepCopy()
	replacement.ResourceVersion = ""
	replacement.UID = types.UID("registration-2")
	if err := store.CreateWorkload(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.WorkloadDeleted(ctx, obj.Name, obj.UID); err != nil || !deleted {
		t.Fatalf("old UID after re-registration = %v, %v", deleted, err)
	}
	if deleted, err := store.WorkloadDeleted(ctx, replacement.Name, replacement.UID); err != nil || deleted {
		t.Fatalf("new UID after re-registration = %v, %v", deleted, err)
	}
}
