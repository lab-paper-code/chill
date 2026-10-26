package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAttemptJobCleanupRequiresPersistedFinalEvidence(t *testing.T) {
	ctx := context.Background()
	fixture := newAttemptFinalizerFixture(t, acceptedAttempt, batchv1.JobComplete)
	job := fixture.progress.Job
	metav1.SetMetaDataAnnotation(&job.ObjectMeta, transitionDirectionIndexAnnotation, "0")
	kube := fixture.finalizer.reader.(client.Client)
	if err := kube.Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	tracking := &cleanupTrackingClient{Client: kube}
	cleanup := &attemptJobCleanup{client: tracking, finalizer: fixture.finalizer, failedJobRetentionSeconds: 123}
	assertPreserved := func() {
		t.Helper()
		if pending, err := cleanup.cleanup(ctx, fixture.measurement, fixture.prepared.EvidenceStore); pending || err != nil {
			t.Fatalf("unfinalized cleanup pending=%v err=%v", pending, err)
		}
		current := &batchv1.Job{}
		if err := kube.Get(ctx, client.ObjectKeyFromObject(job), current); err != nil ||
			current.Spec.TTLSecondsAfterFinished != nil || tracking.deletes != 0 {
			t.Fatalf("unfinalized Job changed: job=%+v err=%v deletes=%d", current, err, tracking.deletes)
		}
	}
	assertPreserved()
	fixture.store.putErr = errors.New("store unavailable")
	if _, err := fixture.finalizer.Finalize(ctx, fixture.measurement, fixture.prepared, 0, 1, fixture.progress); err == nil {
		t.Fatal("finalization unexpectedly succeeded")
	}
	assertPreserved()
	fixture.store.putErr = nil
	finalized, err := fixture.finalizer.Finalize(ctx, fixture.measurement, fixture.prepared, 0, 1, fixture.progress)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := cleanup.cleanup(ctx, fixture.measurement, fixture.prepared.EvidenceStore); pending || err != nil {
		t.Fatalf("finalized cleanup pending=%v err=%v", pending, err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("successful attempt was not deleted: %v", err)
	}
	assertBackgroundUIDDelete(t, tracking, job)
	if finalized.RawReference != fixture.rawReference || fixture.store.objects[finalized.FinalReference.Location()] == nil ||
		fixture.store.objects[finalized.RawReference.Location()] == nil {
		t.Fatal("cleanup altered evidence or its identity")
	}
}

func TestAttemptJobCleanupFailureDoesNotFailMeasurementAndReplaysAfterRestart(t *testing.T) {
	ctx := context.Background()
	attempt := newAttemptFinalizerFixture(t, acceptedAttempt, batchv1.JobComplete)
	finalized, err := attempt.finalizer.Finalize(ctx, attempt.measurement, attempt.prepared, 0, 1, attempt.progress)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newReconcilerFixture(t, measurementStageComplete)
	job := attempt.progress.Job.DeepCopy()
	job.ResourceVersion = ""
	metav1.SetMetaDataAnnotation(&job.ObjectMeta, transitionDirectionIndexAnnotation, "0")
	if err := fixture.kube.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	tracking := &cleanupTrackingClient{Client: fixture.kube, deleteErr: errors.New("delete unavailable")}
	fixture.reconciler.cleanup = &attemptJobCleanup{
		client: tracking, finalizer: attempt.finalizer, failedJobRetentionSeconds: 123,
	}
	result, err := fixture.reconciler.Reconcile(ctx, fixture.request)
	if err != nil || result.RequeueAfter != admissionWait {
		t.Fatalf("cleanup failure result=%+v err=%v", result, err)
	}
	if tracking.deletes != 1 {
		t.Fatalf("cleanup did not reach the injected delete failure: deletes=%d", tracking.deletes)
	}
	current := fixture.current(t)
	if current.Status.Phase != edgev1alpha1.PowerModeTransitionCostPhaseComplete || current.Status.Result == nil {
		t.Fatalf("cleanup failure changed success: %+v", current.Status)
	}
	status := current.Status.DeepCopy()
	if err := tracking.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); err != nil {
		t.Fatal("failed cleanup removed Job:", err)
	}
	tracking.deleteErr = nil
	restartedFinalizer, err := NewAttemptFinalizer(tracking, attempt.store, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newPowerModeTransitionCostReconciler(fixture.kube, fixture.sequence, fixture.writer,
		&attemptJobCleanup{client: tracking, finalizer: restartedFinalizer, failedJobRetentionSeconds: 123})
	if err != nil {
		t.Fatal(err)
	}
	result, err = restarted.Reconcile(ctx, fixture.request)
	if err != nil || result.RequeueAfter != 0 || result.Requeue {
		t.Fatalf("restart cleanup result=%+v err=%v", result, err)
	}
	if err := tracking.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("restart left finalized Job: %v", err)
	}
	assertBackgroundUIDDelete(t, tracking, job)
	if fixture.sequence.calls != 1 || fixture.writer.calls != 1 || attempt.store.puts != 1 ||
		!reflect.DeepEqual(*status, fixture.current(t).Status) ||
		attempt.store.objects[finalized.FinalReference.Location()] == nil ||
		attempt.store.objects[finalized.RawReference.Location()] == nil {
		t.Fatal("restart changed terminal status, replayed measurement, or lost evidence")
	}
}

func TestAttemptJobCleanupRetainsFinalizedDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name      string
		outcome   finalizationOutcome
		condition batchv1.JobConditionType
	}{
		{"failed Job", invalidAttempt, batchv1.JobFailed},
		{"failed observation", failedAttempt, batchv1.JobComplete},
		{"insufficient observation", insufficientAttempt, batchv1.JobFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newAttemptFinalizerFixture(t, test.outcome, test.condition)
			job := fixture.progress.Job
			metav1.SetMetaDataAnnotation(&job.ObjectMeta, transitionDirectionIndexAnnotation, "0")
			kube := fixture.finalizer.reader.(client.Client)
			if err := kube.Update(ctx, job); err != nil {
				t.Fatal(err)
			}
			cleanup := &attemptJobCleanup{client: kube, finalizer: fixture.finalizer, failedJobRetentionSeconds: 321}
			if _, err := cleanup.cleanup(ctx, fixture.measurement, fixture.prepared.EvidenceStore); err != nil {
				t.Fatal(err)
			}
			current := &batchv1.Job{}
			if err := kube.Get(ctx, client.ObjectKeyFromObject(job), current); err != nil || current.Spec.TTLSecondsAfterFinished != nil {
				t.Fatalf("TTL patched before finalization: job=%+v err=%v", current, err)
			}
			if _, err := fixture.finalizer.Finalize(ctx, fixture.measurement, fixture.prepared, 0, 1, fixture.progress); err != nil {
				t.Fatal(err)
			}
			if pending, err := cleanup.cleanup(ctx, fixture.measurement, fixture.prepared.EvidenceStore); pending || err != nil {
				t.Fatalf("diagnostic cleanup pending=%v err=%v", pending, err)
			}
			if err := kube.Get(ctx, client.ObjectKeyFromObject(job), current); err != nil ||
				current.Spec.TTLSecondsAfterFinished == nil || *current.Spec.TTLSecondsAfterFinished != 321 || current.UID != job.UID {
				t.Fatalf("diagnostic retention: job=%+v err=%v", current, err)
			}
		})
	}
}

func TestTerminalMeasurementFailureRetriesRetentionWithoutAttemptReceipt(t *testing.T) {
	ctx := context.Background()
	fixture := newReconcilerFixture(t, measurementStageFailed)
	selection, err := selectAttempt(fixture.measurement, attemptEvidenceStore, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace: fixture.measurement.Namespace, Name: selection.name, UID: "failed-job",
		Annotations: map[string]string{transitionDirectionIndexAnnotation: "0"},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "PowerModeTransitionCost",
			Name: fixture.measurement.Name, UID: fixture.measurement.UID, Controller: ptr.To(true),
		}},
	}, Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: "True"}}}}
	if err := fixture.kube.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	tracking := &cleanupTrackingClient{Client: fixture.kube, patchErr: errors.New("patch unavailable")}
	fixture.reconciler.cleanup.client = tracking
	result, err := fixture.reconciler.Reconcile(ctx, fixture.request)
	if err != nil || result.RequeueAfter != admissionWait || fixture.current(t).Status.Result == nil {
		t.Fatalf("failed measurement finalization result=%+v err=%v", result, err)
	}
	current := &batchv1.Job{}
	if err := fixture.kube.Get(ctx, client.ObjectKeyFromObject(job), current); err != nil || current.Spec.TTLSecondsAfterFinished != nil {
		t.Fatalf("failed retention patch changed Job: job=%+v err=%v", current, err)
	}
	tracking.patchErr = nil
	restarted, err := newPowerModeTransitionCostReconciler(fixture.kube, fixture.sequence, fixture.writer, fixture.reconciler.cleanup)
	if err != nil {
		t.Fatal(err)
	}
	result, err = restarted.Reconcile(ctx, fixture.request)
	if err != nil || result.RequeueAfter != 0 || fixture.sequence.calls != 1 || fixture.writer.calls != 1 {
		t.Fatalf("terminal retention replay result=%+v err=%v", result, err)
	}
	if err := fixture.kube.Get(ctx, client.ObjectKeyFromObject(job), current); err != nil ||
		current.Spec.TTLSecondsAfterFinished == nil || *current.Spec.TTLSecondsAfterFinished != 86400 || tracking.deletes != 0 {
		t.Fatalf("terminal retention replay: job=%+v err=%v", current, err)
	}
}

type cleanupTrackingClient struct {
	client.Client
	deleteErr error
	patchErr  error
	deletes   int
	options   client.DeleteOptions
}

func (c *cleanupTrackingClient) Delete(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
	c.deletes++
	c.options = client.DeleteOptions{}
	for _, option := range options {
		option.ApplyToDelete(&c.options)
	}
	if c.deleteErr != nil {
		return c.deleteErr
	}
	return c.Client.Delete(ctx, object, options...)
}

func (c *cleanupTrackingClient) Patch(ctx context.Context, object client.Object, patch client.Patch, options ...client.PatchOption) error {
	if c.patchErr != nil {
		return c.patchErr
	}
	return c.Client.Patch(ctx, object, patch, options...)
}

func assertBackgroundUIDDelete(t *testing.T, kube *cleanupTrackingClient, job *batchv1.Job) {
	t.Helper()
	if kube.deletes == 0 || kube.options.PropagationPolicy == nil ||
		*kube.options.PropagationPolicy != metav1.DeletePropagationBackground ||
		kube.options.Preconditions == nil || kube.options.Preconditions.UID == nil ||
		*kube.options.Preconditions.UID != job.UID {
		t.Fatalf("unsafe attempt deletion: %+v", kube.options)
	}
}
