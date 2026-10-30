package controller

import (
	"context"
	"errors"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type failingJobReader struct {
	client.Reader
	err error
}

func (r failingJobReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

func TestRecordedJobUsesAuthoritativeReaderNotCache(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "cache miss is not execution loss", false: "real deletion remains execution loss"}[present], func(t *testing.T) {
			scheme := runReconcilerTestScheme(t)
			run := testRun()
			recordRunConfigAndJob(run)
			resolved := resolvedRunForPrepared(t, currentPreparedRun(t))
			job := recordedJob(t, run, resolved, &batchv1.JobStatus{Active: 1})
			// The cached client never sees the Job; only the API reader does.
			cached := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if present {
				builder = builder.WithObjects(job)
			}
			api := builder.Build()
			resolver, jobs := &recordingRunConfigResolver{}, &recordingExecutionJobs{}
			r, err := NewRunReconciler(cached, api, resolver.Resolve, jobs.Find, jobs.Ensure, (&recordingRunEvidenceFinalizer{}).Finalize, (&recordingRunEvidenceFinalizer{}).RuntimeTermination)
			if err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(run)
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			actual := &edge.ProfilingRun{}
			if err := cached.Get(context.Background(), key, actual); err != nil {
				t.Fatal(err)
			}
			condition := findRunCondition(actual.Status.Conditions, edge.ProfilingRunConditionExecutionComplete)
			want := "ExecutionLost"
			if present {
				want = "JobRunning"
			}
			if condition == nil || condition.Reason != want {
				t.Fatalf("condition=%+v want %s", condition, want)
			}
			if resolver.calls != 0 || jobs.calls != 0 {
				t.Fatalf("cache miss created extra work: resolver=%d jobs=%d", resolver.calls, jobs.calls)
			}
		})
	}
}

func TestRecordedJobTransientReaderErrorDoesNotPublishLoss(t *testing.T) {
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	recordRunConfigAndJob(run)
	cached := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	want := errors.New("temporary API transport failure")
	r, err := NewRunReconciler(cached, failingJobReader{Reader: cached, err: want}, (&recordingRunConfigResolver{}).Resolve, (&recordingExecutionJobs{}).Find, (&recordingExecutionJobs{}).Ensure, (&recordingRunEvidenceFinalizer{}).Finalize, (&recordingRunEvidenceFinalizer{}).RuntimeTermination)
	if err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKeyFromObject(run)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); !errors.Is(err, want) {
		t.Fatalf("error=%v want %v", err, want)
	}
	actual := &edge.ProfilingRun{}
	if err := cached.Get(context.Background(), key, actual); err != nil {
		t.Fatal(err)
	}
	for _, condition := range actual.Status.Conditions {
		if condition.Type == edge.ProfilingRunConditionExecutionComplete && condition.Status == metav1.ConditionFalse && condition.Reason == "ExecutionLost" {
			t.Fatal("transport failure published execution loss")
		}
	}
}
