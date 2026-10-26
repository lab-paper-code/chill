package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type recordingRunConfigResolver struct {
	calls  int
	run    *edgev1alpha1.ProfilingRun
	result ResolvedRun
	err    error
}

func (f *recordingRunConfigResolver) Resolve(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
) (ResolvedRun, error) {
	f.calls++
	f.run = run.DeepCopy()
	return f.result, f.err
}

type recordingExecutionJobs struct {
	calls    int
	resolved ResolvedRun
	job      *batchv1.Job
	err      error
}

func (j *recordingExecutionJobs) Find(
	context.Context,
	*edgev1alpha1.ProfilingRun,
) (*batchv1.Job, *ResolvedRun, error) {
	return nil, nil, nil
}

type recordingRunEvidenceFinalizer struct {
	calls       int
	result      FinalizedRunEvidence
	err         error
	termination *edgev1alpha1.ProfilingRunRuntimeTermination
}

func (f *recordingRunEvidenceFinalizer) Finalize(
	context.Context,
	*edgev1alpha1.ProfilingRun,
	profiler.PreparedRun,
	*batchv1.Job,
) (FinalizedRunEvidence, error) {
	f.calls++
	return f.result, f.err
}

func (f *recordingRunEvidenceFinalizer) RuntimeTermination(
	context.Context,
	*edgev1alpha1.ProfilingRun,
	*batchv1.Job,
) (*edgev1alpha1.ProfilingRunRuntimeTermination, error) {
	return f.termination, nil
}

func (j *recordingExecutionJobs) Ensure(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
	resolved ResolvedRun,
) (*batchv1.Job, bool, error) {
	j.calls++
	j.resolved = resolved
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

func TestRunReconcilerResolvesThenRecordsOneMeasurementJob(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	grant(run)
	prepared := currentPreparedRun(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	resolver := &recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, resolver, jobs, &recordingRunEvidenceFinalizer{})
	if err != nil {
		t.Fatal(err)
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || jobs.calls != 1 || !reflect.DeepEqual(jobs.resolved.Prepared.RunConfig, prepared.RunConfig) {
		t.Fatalf("unexpected transition calls: resolver=%d jobs=%d", resolver.calls, jobs.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.Job == nil || actual.Status.Job.Name != "measurement-job" || actual.Status.Job.UID != "job-uid" {
		t.Fatalf("Job identity not recorded: %#v", actual.Status)
	}
}

func TestRunReconcilerEndsWithoutJobWhenRequiredStateIsUnconfirmed(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, resolved := testActuatedRunAndResolved(t)
	grant(run)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{result: resolved}, jobs, &recordingRunEvidenceFinalizer{})
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if jobs.calls != 0 {
		t.Fatalf("unconfirmed Run created %d Jobs", jobs.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	condition := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
	if condition == nil || condition.Status != metav1.ConditionFalse ||
		condition.Reason != requiredStateNotConfirmedReason {
		t.Fatalf("unconfirmed state outcome = %#v", actual.Status)
	}
}

func TestRunReconcilerRejectsNodeSettledInAnotherMode(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, resolved := testActuatedRunAndResolved(t)
	grant(run)
	node := settledNode(resolved.Prepared.RunConfig, "MAXN", time.Now().Add(-time.Minute), 2)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).
		WithObjects(run, node).Build()
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{result: resolved}, jobs, &recordingRunEvidenceFinalizer{})
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if jobs.calls != 0 {
		t.Fatalf("Run ignored the newest state and created %d Jobs", jobs.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	condition := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
	if actual.Status.Job != nil || condition == nil ||
		condition.Status != metav1.ConditionFalse || condition.Reason != requiredStateNotConfirmedReason {
		t.Fatalf("newest different state outcome = %#v", actual.Status)
	}
}

func TestRunReconcilerCreatesJobOnceNodeSettledRequiredMode(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, resolved := testActuatedRunAndResolved(t)
	grant(run)
	node := settledNode(
		resolved.Prepared.RunConfig, resolved.Prepared.RunConfig.Actuation.PowerMode, time.Now().Add(-time.Minute), 2,
	)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).
		WithObjects(run, node).Build()
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{result: resolved}, jobs, &recordingRunEvidenceFinalizer{})
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if jobs.calls != 1 {
		t.Fatalf("confirmed Run created %d Jobs, want 1", jobs.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.Job == nil || actual.Status.Job.Name != "measurement-job" {
		t.Fatalf("confirmed Run references = %#v", actual.Status)
	}
}

func TestRunReconcilerFinalizesActuatedEvidenceAfterReadyTarget(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, resolved := testActuatedRunAndResolved(t)
	recordRunConfigAndJob(run)
	completedAt := metav1.NewTime(time.Date(2026, 8, 6, 12, 0, 30, 0, time.UTC))
	job := recordedJob(t, run, resolved, &batchv1.JobStatus{
		Succeeded:      1,
		CompletionTime: &completedAt,
		Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: completedAt,
		}},
	})
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
	finalized := FinalizedRunEvidence{
		Binding:       testFinalizedStoreBinding(),
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Reference: evidencestore.ObjectIdentity{
			Store: "chill-evidence-store", Key: "runs/run-uid/execution.json",
			Size: 1024,
		},
		Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "run-pod", UID: "pod-uid"},
		Node: edgev1alpha1.ProfilingRunObjectReference{Name: "jetsono", UID: "node-uid"},
	}
	finalizer := &recordingRunEvidenceFinalizer{result: finalized}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{result: resolved}, &recordingExecutionJobs{}, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("finalizer calls = %d, want 1", finalizer.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.Evidence == nil || actual.Status.Evidence.Key != finalized.Reference.Key ||
		actual.Status.Evidence.SchemaVersion != profilerevidence.ExecutionEvidenceSchemaVersion {
		t.Fatalf("finalized actuated status = %#v", actual.Status)
	}
}

func TestRunReconcilerDoesNotRefinalizePermanentTargetMismatch(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, resolved := testActuatedRunAndResolved(t)
	recordRunConfigAndJob(run)
	completedAt := metav1.NewTime(time.Date(2026, 8, 6, 12, 0, 30, 0, time.UTC))
	job := recordedJob(t, run, resolved, &batchv1.JobStatus{
		Succeeded:      1,
		CompletionTime: &completedAt,
		Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: completedAt,
		}},
	})
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
	finalizeErr := fmt.Errorf(
		"%w: actuation target observation does not match RunConfig",
		profilerevidence.ErrPermanentValidation,
	)
	finalizer := &recordingRunEvidenceFinalizer{err: finalizeErr}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{result: resolved}, &recordingExecutionJobs{}, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil || result != (ctrl.Result{}) {
		t.Fatalf("target mismatch result = %#v, error = %v", result, err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("finalizer calls = %d, want 1", finalizer.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	execution := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
	evidence := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionEvidenceVerified)
	if actual.Status.Evidence != nil || execution == nil || execution.Status != metav1.ConditionTrue ||
		execution.Reason != "JobSucceeded" || evidence == nil || evidence.Status != metav1.ConditionFalse ||
		evidence.Reason != evidenceReasonInvalid ||
		!strings.Contains(evidence.Message, "actuation target observation does not match RunConfig") {
		t.Fatalf("terminal target mismatch status = %#v", actual.Status)
	}
	result, err = reconciler.Reconcile(context.Background(), request)
	if err != nil || result != (ctrl.Result{}) {
		t.Fatalf("terminal target mismatch re-entry result = %#v, error = %v", result, err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("finalizer calls after terminal re-entry = %d, want 1", finalizer.calls)
	}
}

func TestRunReconcilerRetriesTransientEvidenceFinalizationFailure(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	prepared := currentPreparedRun(t)
	recordRunConfigAndJob(run)
	job := recordedJob(t, run, resolvedRunForPrepared(t, prepared), &batchv1.JobStatus{
		Succeeded:  1,
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
	})
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
	want := errors.New("evidence store temporarily unavailable")
	finalizer := &recordingRunEvidenceFinalizer{err: want}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)}, &recordingExecutionJobs{}, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	result, err := reconciler.Reconcile(context.Background(), request)
	if !errors.Is(err, want) || result != (ctrl.Result{}) {
		t.Fatalf("transient finalization result = %#v, error = %v, want wrapped %v", result, err, want)
	}
	if !strings.Contains(err.Error(), "finalize ProfilingRun chill-system/run evidence") {
		t.Fatalf("transient finalization error context = %q", err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("finalizer calls = %d, want 1", finalizer.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	evidence := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionEvidenceVerified)
	if actual.Status.Evidence != nil || evidence == nil || evidence.Status != metav1.ConditionFalse ||
		evidence.Reason != evidenceReasonVerificationFailed ||
		evidence.Message != "retained execution evidence could not be verified" {
		t.Fatalf("retryable finalization status = %#v", actual.Status)
	}
}

func TestRunReconcilerDoesNotMaterializeASecondRecordedJob(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	run.Status.Job = &edgev1alpha1.ProfilingRunObjectReference{Name: "existing", UID: "existing-uid"}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	resolver := &recordingRunConfigResolver{}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, resolver, jobs, &recordingRunEvidenceFinalizer{})
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 0 || jobs.calls != 0 {
		t.Fatalf("recorded Job triggered materialization: resolver=%d jobs=%d", resolver.calls, jobs.calls)
	}
}

func TestRunReconcilerFinalizesSuccessfulJobAndPublishesCompactStatus(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	prepared := currentPreparedRun(t)
	recordRunConfigAndJob(run)
	job := recordedJob(t, run, resolvedRunForPrepared(t, prepared), &batchv1.JobStatus{
		Succeeded:  1,
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
	})
	finalized := FinalizedRunEvidence{
		Binding:       testFinalizedStoreBinding(),
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Reference: evidencestore.ObjectIdentity{
			Store: "chill-evidence-store", Key: "runs/run-uid/execution.json",
			Size: 1024,
		},
		Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "run-pod", UID: "pod-uid"},
		Node: edgev1alpha1.ProfilingRunObjectReference{Name: "lattepanda", UID: "node-uid"},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
	finalizer := &recordingRunEvidenceFinalizer{result: finalized}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)}, &recordingExecutionJobs{}, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("finalizer calls = %d, want 1", finalizer.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.Evidence == nil || actual.Status.Evidence.Key != finalized.Reference.Key ||
		actual.Status.Evidence.SchemaVersion != profilerevidence.ExecutionEvidenceSchemaVersion ||
		actual.Status.JobCleanup != runJobCleanupDone || !reflect.DeepEqual(actual.Status.EvidenceStore, finalized.Binding) ||
		actual.Status.ObservedExecution == nil || actual.Status.ObservedExecution.Pod != finalized.Pod ||
		actual.Status.ObservedExecution.Node != finalized.Node {
		t.Fatalf("finalized status = %#v", actual.Status)
	}
	execution := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
	evidence := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionEvidenceVerified)
	if execution == nil || execution.Status != metav1.ConditionTrue || execution.Reason != "JobSucceeded" ||
		evidence == nil || evidence.Status != metav1.ConditionTrue || evidence.Reason != "EvidenceVerified" {
		t.Fatalf("finalized conditions = %#v", actual.Status.Conditions)
	}
	// Explicit evidence cleanup does not clear the native terminal reference.
	// A replay must not ask the finalizer to reconstruct the removed objects.
	finalizer.err = errors.New("stored evidence intentionally removed")
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: job.Namespace, Name: job.Name}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("verified Job was not removed: %v", err)
	}
	wantStatus := actual.DeepCopy().Status
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if finalizer.calls != 1 {
		t.Fatalf("terminal Run replay regenerated evidence: finalizer calls=%d", finalizer.calls)
	}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual.Status, wantStatus) {
		t.Fatalf("Job removal changed terminal status: got %#v, want %#v", actual.Status, wantStatus)
	}
}

func TestRunReconcilerRecordsRuntimeTerminationOfFailedJob(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	prepared := currentPreparedRun(t)
	recordRunConfigAndJob(run)
	job := recordedJob(t, run, resolvedRunForPrepared(t, prepared), &batchv1.JobStatus{
		Failed:     1,
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
	})
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
	want := edgev1alpha1.ProfilingRunRuntimeTermination{
		ExitCode: 3, LogTail: "ERROR [runtime] execute firstfloor measurement: timed out\n",
	}
	finalizer := &recordingRunEvidenceFinalizer{termination: &want}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)}, &recordingExecutionJobs{}, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	// The Pod and its termination state may be deleted after the first record.
	finalizer.termination = nil
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.RuntimeTermination == nil || *actual.Status.RuntimeTermination != want {
		t.Fatalf("runtime termination = %#v, want %#v", actual.Status.RuntimeTermination, want)
	}
	execution := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
	if execution == nil || execution.Status != metav1.ConditionFalse || execution.Reason != "JobFailed" {
		t.Fatalf("execution condition = %#v", execution)
	}
}

func recordedJob(
	t *testing.T,
	run *edgev1alpha1.ProfilingRun,
	resolved ResolvedRun,
	status *batchv1.JobStatus,
) *batchv1.Job {
	t.Helper()
	job := ownedJob(run, status)
	config, err := resolved.Prepared.RunConfig.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	executionPolicy, err := resolved.ExecutionPolicy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	job.Annotations = map[string]string{
		executionPolicyAnnotation: string(executionPolicy),
	}
	if resolved.Prepared.ActuationPolicy != nil {
		policy, err := resolved.Prepared.ActuationPolicy.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		job.Annotations[actuationPolicyAnnotation] = string(policy)
	}
	job.Spec.Template.Spec.Containers = []corev1.Container{{
		Name: runtimeContainerName, Args: []string{runConfigArgumentPrefix + string(config)},
	}}
	return job
}

func TestRunReconcilerIgnoresMissingRun(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	resolver := &recordingRunConfigResolver{}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, resolver, jobs, &recordingRunEvidenceFinalizer{})
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "chill-system", Name: "missing"}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 0 || jobs.calls != 0 {
		t.Fatalf("missing Run triggered work: resolver=%d jobs=%d", resolver.calls, jobs.calls)
	}
}

func TestRunReconcilerReturnsTransitionErrors(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	grant(run)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	want := errors.New("unresolved execution facts")
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, &recordingRunConfigResolver{err: want}, &recordingExecutionJobs{}, &recordingRunEvidenceFinalizer{})
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); !errors.Is(err, want) {
		t.Fatalf("Reconcile() error = %v, want wrapped %v", err, want)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.ObservedGeneration != 0 || len(actual.Status.Conditions) != 0 {
		t.Fatalf("retryable freeze failure changed status: %#v", actual.Status)
	}
}

func TestRunReconcilerPublishesTerminalResolutionFailureWithoutRequeue(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	grant(run)
	run.Generation = 7
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	resolver := &recordingRunConfigResolver{
		err: profiler.InvalidExecutionPolicy("execution policy %q is not configured", "typo-v9"),
	}
	jobs := &recordingExecutionJobs{}
	finalizer := &recordingRunEvidenceFinalizer{}
	reconciler, err := NewRunReconciler(kubeClient, kubeClient, resolver, jobs, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	result, err := reconciler.Reconcile(context.Background(), request)
	if err != nil || result != (ctrl.Result{}) {
		t.Fatalf("terminal freeze result = %#v, error = %v", result, err)
	}
	if resolver.calls != 1 || jobs.calls != 0 {
		t.Fatalf("terminal freeze calls: resolver=%d jobs=%d", resolver.calls, jobs.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	condition := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
	if actual.Status.ObservedGeneration != run.Generation || condition == nil ||
		condition.Status != metav1.ConditionFalse || condition.ObservedGeneration != run.Generation ||
		condition.Reason != profiler.ResolutionReasonExecutionPolicyInvalid ||
		!strings.Contains(condition.Message, `execution policy "typo-v9" is not configured`) {
		t.Fatalf("terminal freeze status = %#v", actual.Status)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || jobs.calls != 0 || finalizer.calls != 0 {
		t.Fatalf("terminal pre-Job replay: resolves=%d creates=%d finalizes=%d",
			resolver.calls, jobs.calls, finalizer.calls)
	}
}

func runReconcilerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{batchv1.AddToScheme, corev1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func resolvedRunForPrepared(t *testing.T, prepared profiler.PreparedRun) ResolvedRun {
	t.Helper()
	executionPolicy, err := profiler.FreezeConfiguredExecutionPolicy(
		controllerExecutionPolicyCatalog(t),
		prepared.RunConfig,
		profiler.ExecutionPolicySource{
			Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: testSystemNamespace,
			Name: "chill-profiler-facts", UID: testFactsUID, ResourceVersion: "42",
			Key: "execution-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return ResolvedRun{
		Prepared:        prepared,
		ExecutionPolicy: executionPolicy,
	}
}

func findRunCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// grant records ChillServer's admission of a root Run.
func grant(run *edgev1alpha1.ProfilingRun) {
	run.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}
}

func TestRunReconcilerWaitsForAdmissionOfRootRunOnly(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, admission, owner string
		requeue                time.Duration
		reason                 string
		jobs                   int
	}{
		{"root not admitted", "", "", 5 * time.Second, "", 0},
		{"root refused", profilingrun.RefusedAdmission("NodeServing", "serving/a"), "", 0, "NodeServing", 0},
		{"campaign child", "", "BatchEnergyOptimization", 0, "", 1},
		{"idle baseline child", "", "ModelLoadedIdleBaseline", 0, "", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := testRun()
			run.Annotations = map[string]string{profilingrun.AdmissionAnnotation: test.admission}
			if test.owner != "" {
				run.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: edgev1alpha1.GroupVersion.String(), Kind: test.owner,
					Name: "root", UID: "root-uid", Controller: ptr.To(true),
				}}
			}
			kubeClient := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run).Build()
			resolver := &recordingRunConfigResolver{result: resolvedRunForPrepared(t, currentPreparedRun(t))}
			jobs := &recordingExecutionJobs{}
			reconciler, err := NewRunReconciler(kubeClient, kubeClient, resolver, jobs, &recordingRunEvidenceFinalizer{})
			if err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
			result, err := reconciler.Reconcile(context.Background(), request)
			if err != nil || result.RequeueAfter != test.requeue || jobs.calls != test.jobs {
				t.Fatalf("result=%#v err=%v jobs=%d", result, err, jobs.calls)
			}
			actual := &edgev1alpha1.ProfilingRun{}
			if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
				t.Fatal(err)
			}
			condition := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
			if test.reason == "" {
				if test.jobs == 0 && (condition != nil || resolver.calls != 0) {
					t.Fatalf("waiting Run progressed: %#v", actual.Status)
				}
				return
			}
			if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != test.reason || jobs.calls != 0 {
				t.Fatalf("refused Run status = %#v", actual.Status)
			}
		})
	}
}

func testFinalizedStoreBinding() *edgev1alpha1.ProfilingRunEvidenceStoreBinding {
	return &edgev1alpha1.ProfilingRunEvidenceStoreBinding{
		Store: "chill-evidence-store", Endpoint: "http://evidence-store", Bucket: "chill",
		Region: "us-east-1", CredentialsSecretName: "evidence-credentials",
	}
}
