package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type recordingIntentFreezer struct {
	calls  int
	run    *edgev1alpha1.ProfilingRun
	result FreezeIntentResult
	err    error
}

func (f *recordingIntentFreezer) Freeze(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
) (FreezeIntentResult, error) {
	f.calls++
	f.run = run.DeepCopy()
	return f.result, f.err
}

type recordingExecutionJobs struct {
	calls    int
	prepared profiler.PreparedRun
	job      *batchv1.Job
	err      error
}

func (j *recordingExecutionJobs) Ensure(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
) (*batchv1.Job, bool, error) {
	j.calls++
	j.prepared = prepared
	if j.err != nil {
		return nil, false, j.err
	}
	if j.job != nil {
		return j.job.DeepCopy(), true, nil
	}
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace: run.Namespace,
		Name:      "measurement-job",
		UID:       "job-uid",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "ProfilingRun",
			Name: run.Name, UID: run.UID, Controller: ptr.To(true),
		}},
	}}, true, nil
}

func TestRunReconcilerFreezesThenRecordsOneMeasurementJob(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	prepared := currentPreparedRun(t)
	run.Status.IntentDigest = prepared.IntentDigest
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	freezer := &recordingIntentFreezer{result: FreezeIntentResult{PreparedRun: prepared}}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, freezer, jobs)
	if err != nil {
		t.Fatal(err)
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if freezer.calls != 1 || jobs.calls != 1 || jobs.prepared.IntentDigest != prepared.IntentDigest {
		t.Fatalf("unexpected transition calls: freezer=%d jobs=%d", freezer.calls, jobs.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.Job == nil || actual.Status.Job.Name != "measurement-job" || actual.Status.Job.UID != "job-uid" {
		t.Fatalf("Job identity not recorded: %#v", actual.Status)
	}
}

func TestRunReconcilerDoesNotMaterializeASecondRecordedJob(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	run.Status.IntentDigest = "sha256:" + strings.Repeat("a", 64)
	run.Status.Job = &edgev1alpha1.ProfilingRunObjectReference{Name: "existing", UID: "existing-uid"}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	freezer := &recordingIntentFreezer{}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, freezer, jobs)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if freezer.calls != 1 || jobs.calls != 0 {
		t.Fatalf("recorded Job triggered materialization: freezer=%d jobs=%d", freezer.calls, jobs.calls)
	}
}

func TestRunReconcilerIgnoresMissingRun(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	freezer := &recordingIntentFreezer{}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, freezer, jobs)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "chill-system", Name: "missing"}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if freezer.calls != 0 || jobs.calls != 0 {
		t.Fatalf("missing Run triggered work: freezer=%d jobs=%d", freezer.calls, jobs.calls)
	}
}

func TestRunReconcilerReturnsTransitionErrors(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	want := errors.New("unresolved execution facts")
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingIntentFreezer{err: want},
		&recordingExecutionJobs{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); !errors.Is(err, want) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, want)
	}
}

func runReconcilerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{batchv1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}
