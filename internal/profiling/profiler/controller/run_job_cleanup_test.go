package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	batch "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type cleanupRecordingClient struct {
	client.Client
	failure error
	deletes int
	options client.DeleteOptions
}

func (c *cleanupRecordingClient) Delete(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
	c.deletes++
	c.options = *(&client.DeleteOptions{}).ApplyOptions(options)
	if c.failure != nil {
		return c.failure
	}
	return c.Client.Delete(ctx, object, options...)
}

func TestVerifiedRunRetriesBackgroundCleanupWithoutChangingStatus(t *testing.T) {
	run := testRun()
	recordRunConfigAndJob(run)
	run.Status.JobCleanup = runJobCleanupPending
	run.Status.Evidence = &edge.ProfilingRunEvidenceReference{Store: "evidence", Key: "execution.json"}
	run.Status.Conditions = []meta.Condition{{Type: edge.ProfilingRunConditionEvidenceVerified, Status: meta.ConditionTrue}}
	job := &batch.Job{ObjectMeta: meta.ObjectMeta{Name: run.Status.Job.Name, Namespace: run.Namespace, UID: types.UID(run.Status.Job.UID)}}
	kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run, job).Build()
	recording := &cleanupRecordingClient{Client: kube, failure: errors.New("temporary delete failure")}
	resolver, jobs, finalizer := &recordingRunConfigResolver{}, &recordingExecutionJobs{}, &recordingRunEvidenceFinalizer{}
	r, err := NewRunReconciler(recording, kube, resolver, jobs, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
	result, err := r.Reconcile(context.Background(), request)
	if err != nil || result.RequeueAfter <= 0 {
		t.Fatalf("cleanup failure result = %#v, %v", result, err)
	}
	actual := &edge.ProfilingRun{}
	if err := kube.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual.Status, run.Status) {
		t.Fatalf("cleanup failure changed status: %#v", actual.Status)
	}
	if recording.options.PropagationPolicy == nil || *recording.options.PropagationPolicy != meta.DeletePropagationBackground ||
		recording.options.Preconditions == nil || recording.options.Preconditions.UID == nil || string(*recording.options.Preconditions.UID) != run.Status.Job.UID {
		t.Fatalf("delete options = %#v", recording.options)
	}
	// A new reconciler simulates restart after status publication but before deletion.
	recording.failure = nil
	r, err = NewRunReconciler(recording, kube, resolver, jobs, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(job), &batch.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("cleaned Job lookup = %v", err)
	}
	if resolver.calls != 0 || jobs.calls != 0 || finalizer.calls != 0 {
		t.Fatal("terminal cleanup reconstructed execution")
	}
}

func TestFailedRunAddsRetentionOnlyAfterRecordedFailure(t *testing.T) {
	for _, seconds := range []int32{60, 3600} {
		run := testRun()
		recordRunConfigAndJob(run)
		run.Status.JobCleanup = runJobCleanupPending
		run.Status.Conditions = []meta.Condition{{Type: edge.ProfilingRunConditionExecutionComplete, Status: meta.ConditionFalse, Reason: "JobFailed"}}
		job := ownedJob(run, &batch.JobStatus{})
		job.Name, job.UID = run.Status.Job.Name, types.UID(run.Status.Job.UID)
		if job.Spec.TTLSecondsAfterFinished != nil {
			t.Fatal("Job starts with TTL")
		}
		kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run, job).Build()
		r, err := NewRunReconciler(kube, kube, &recordingRunConfigResolver{}, &recordingExecutionJobs{}, &recordingRunEvidenceFinalizer{})
		if err != nil {
			t.Fatal(err)
		}
		r.FailedJobRetentionSeconds = seconds
		request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
		if _, err := r.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		actual := &batch.Job{}
		if err := kube.Get(context.Background(), client.ObjectKeyFromObject(job), actual); err != nil {
			t.Fatal(err)
		}
		if actual.Spec.TTLSecondsAfterFinished == nil || *actual.Spec.TTLSecondsAfterFinished != seconds {
			t.Fatalf("failed retention = %#v", actual.Spec.TTLSecondsAfterFinished)
		}
		if err := kube.Delete(context.Background(), actual); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reconcile(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		stored := &edge.ProfilingRun{}
		if err := kube.Get(context.Background(), request.NamespacedName, stored); err != nil {
			t.Fatal(err)
		}
		run.Status.JobCleanup = runJobCleanupDone
		if !reflect.DeepEqual(stored.Status, run.Status) {
			t.Fatalf("expired failure changed status: %#v", stored.Status)
		}
	}
}

func TestLegacyTerminalRunsLeaveMeasurementJobsUntouched(t *testing.T) {
	for _, outcome := range []string{"verified", "failed", "invalid"} {
		t.Run(outcome, func(t *testing.T) {
			run := testRun()
			recordRunConfigAndJob(run)
			switch outcome {
			case "verified":
				run.Status.Evidence = &edge.ProfilingRunEvidenceReference{Store: "evidence", Key: "execution.json"}
				run.Status.Conditions = []meta.Condition{{Type: edge.ProfilingRunConditionEvidenceVerified, Status: meta.ConditionTrue}}
			case "failed":
				run.Status.Conditions = []meta.Condition{{Type: edge.ProfilingRunConditionExecutionComplete, Status: meta.ConditionFalse, Reason: "JobFailed"}}
			case "invalid":
				run.Status.Conditions = []meta.Condition{{Type: edge.ProfilingRunConditionEvidenceVerified, Status: meta.ConditionFalse, Reason: evidenceReasonInvalid}}
			}
			job := ownedJob(run, &batch.JobStatus{})
			job.Name, job.UID = run.Status.Job.Name, types.UID(run.Status.Job.UID)
			kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run, job).Build()
			recording := &cleanupRecordingClient{Client: kube}
			r, err := NewRunReconciler(recording, kube, &recordingRunConfigResolver{}, &recordingExecutionJobs{}, &recordingRunEvidenceFinalizer{})
			if err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
			if _, err := r.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			actual := &batch.Job{}
			if err := kube.Get(context.Background(), client.ObjectKeyFromObject(job), actual); err != nil {
				t.Fatal(err)
			}
			if recording.deletes != 0 || actual.Spec.TTLSecondsAfterFinished != nil {
				t.Fatalf("legacy Job was cleaned: deletes=%d, TTL=%v", recording.deletes, actual.Spec.TTLSecondsAfterFinished)
			}
			stored := &edge.ProfilingRun{}
			if err := kube.Get(context.Background(), request.NamespacedName, stored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored.Status, run.Status) {
				t.Fatalf("legacy status changed: %#v", stored.Status)
			}
		})
	}
}
