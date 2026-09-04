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
	profileradmission "github.com/lab-paper-code/chill/internal/profiling/profiler/admission"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	calls  int
	frozen ResolvedRun
	job    *batchv1.Job
	err    error
}

func (j *recordingExecutionJobs) Find(
	context.Context,
	*edgev1alpha1.ProfilingRun,
) (*batchv1.Job, *ResolvedRun, error) {
	return nil, nil, nil
}

type recordingRunEvidenceFinalizer struct {
	calls  int
	result FinalizedRunEvidence
	err    error
}

type recordingRunEvidenceAdmitter struct {
	calls  int
	result profileradmission.Result
	err    error
}

type statusPatchFailureClient struct {
	client.Client
	err error
}

func (c *statusPatchFailureClient) Status() client.SubResourceWriter {
	return &statusPatchFailureWriter{SubResourceWriter: c.Client.Status(), err: c.err}
}

type statusPatchFailureWriter struct {
	client.SubResourceWriter
	err error
}

func (w *statusPatchFailureWriter) Patch(
	context.Context,
	client.Object,
	client.Patch,
	...client.SubResourcePatchOption,
) error {
	return w.err
}

func (a *recordingRunEvidenceAdmitter) Admit(
	context.Context,
	*edgev1alpha1.ProfilingRun,
	ResolvedRun,
	*batchv1.Job,
	evidencestore.ObjectIdentity,
) (profileradmission.Result, error) {
	a.calls++
	return a.result, a.err
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

func (j *recordingExecutionJobs) Ensure(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
	frozen ResolvedRun,
) (*batchv1.Job, bool, error) {
	j.calls++
	j.frozen = frozen
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
	prepared := currentPreparedRun(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	resolver := &recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(
		kubeClient, resolver, jobs,
		&recordingRunEvidenceFinalizer{}, &recordingRunEvidenceAdmitter{},
	)
	if err != nil {
		t.Fatal(err)
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || jobs.calls != 1 || !reflect.DeepEqual(jobs.frozen.Prepared.RunConfig, prepared.RunConfig) {
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
	run, frozen := testActuatedRunAndFrozen(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{result: frozen},
		jobs,
		&recordingRunEvidenceFinalizer{},
		&recordingRunEvidenceAdmitter{},
	)
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
	if actual.Status.Actuation != nil || condition == nil || condition.Status != metav1.ConditionFalse ||
		condition.Reason != requiredStateNotConfirmedReason {
		t.Fatalf("unconfirmed state outcome = %#v", actual.Status)
	}
	transitions := &edgev1alpha1.NodeActuationList{}
	if err := kubeClient.List(context.Background(), transitions); err != nil {
		t.Fatal(err)
	}
	if len(transitions.Items) != 0 {
		t.Fatalf("Run created transitions: %#v", transitions.Items)
	}
}

func TestRunReconcilerUsesNewestSuccessfulTransitionOnly(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, frozen := testActuatedRunAndFrozen(t)
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	matching := completedTransitionForRun(run, frozen, "matching", frozen.Prepared.RunConfig.Actuation.PowerMode, base)
	different := completedTransitionForRun(run, frozen, "different", "MAXN", base.Add(time.Minute))
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).
		WithObjects(run, matching, different).Build()
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{result: frozen},
		jobs,
		&recordingRunEvidenceFinalizer{},
		&recordingRunEvidenceAdmitter{},
	)
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
	if actual.Status.Actuation != nil || actual.Status.Job != nil || condition == nil ||
		condition.Status != metav1.ConditionFalse || condition.Reason != requiredStateNotConfirmedReason {
		t.Fatalf("newest different state outcome = %#v", actual.Status)
	}
}

func TestRunReconcilerRecordsConfirmingTransitionBeforeCreatingJob(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, frozen := testActuatedRunAndFrozen(t)
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	older := completedTransitionForRun(run, frozen, "older", "MAXN", base)
	confirming := completedTransitionForRun(
		run, frozen, "confirming", frozen.Prepared.RunConfig.Actuation.PowerMode, base.Add(time.Minute),
	)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).
		WithObjects(run, older, confirming).Build()
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{result: frozen},
		jobs,
		&recordingRunEvidenceFinalizer{},
		&recordingRunEvidenceAdmitter{},
	)
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
	if actual.Status.Actuation == nil || actual.Status.Actuation.Name != confirming.Name ||
		actual.Status.Actuation.UID != string(confirming.UID) || actual.Status.Job == nil ||
		actual.Status.Job.Name != "measurement-job" {
		t.Fatalf("confirmed Run references = %#v", actual.Status)
	}
	transitions := &edgev1alpha1.NodeActuationList{}
	if err := kubeClient.List(context.Background(), transitions); err != nil {
		t.Fatal(err)
	}
	if len(transitions.Items) != 2 {
		t.Fatalf("Run changed transition count to %d", len(transitions.Items))
	}
}

func TestRunReconcilerFinalizesActuatedEvidenceAfterReadyTarget(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, frozen := testActuatedRunAndFrozen(t)
	recordIntentAndJob(run)
	completedAt := metav1.NewTime(time.Date(2026, 8, 6, 12, 0, 30, 0, time.UTC))
	job := recordedJob(t, run, frozen, &batchv1.JobStatus{
		Succeeded:      1,
		CompletionTime: &completedAt,
		Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: completedAt,
		}},
	})
	handoff := completedTransitionForRun(
		run, frozen, "node-actuation", frozen.Prepared.RunConfig.Actuation.PowerMode,
		time.Date(2026, 8, 6, 11, 59, 0, 0, time.UTC),
	)
	run.Status.Actuation = &edgev1alpha1.ProfilingRunObjectReference{Name: handoff.Name, UID: string(handoff.UID)}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job, handoff).Build()
	finalized := FinalizedRunEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Reference: evidencestore.ObjectIdentity{
			Store: "chill-evidence-store", Key: "runs/run-uid/execution.json",
			Size: 1024,
		},
		Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "run-pod", UID: "pod-uid"},
		Node: edgev1alpha1.ProfilingRunObjectReference{Name: "jetsono", UID: "node-uid"},
	}
	finalizer := &recordingRunEvidenceFinalizer{result: finalized}
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{result: frozen},
		&recordingExecutionJobs{},
		finalizer,
		&recordingRunEvidenceAdmitter{},
	)
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
	run, frozen := testActuatedRunAndFrozen(t)
	recordIntentAndJob(run)
	completedAt := metav1.NewTime(time.Date(2026, 8, 6, 12, 0, 30, 0, time.UTC))
	job := recordedJob(t, run, frozen, &batchv1.JobStatus{
		Succeeded:      1,
		CompletionTime: &completedAt,
		Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: completedAt,
		}},
	})
	handoff := completedTransitionForRun(
		run, frozen, "node-actuation", frozen.Prepared.RunConfig.Actuation.PowerMode,
		time.Date(2026, 8, 6, 11, 59, 0, 0, time.UTC),
	)
	run.Status.Actuation = &edgev1alpha1.ProfilingRunObjectReference{Name: handoff.Name, UID: string(handoff.UID)}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job, handoff).Build()
	finalizeErr := fmt.Errorf(
		"%w: actuation target observation does not match RunConfig",
		profilerevidence.ErrPermanentValidation,
	)
	finalizer := &recordingRunEvidenceFinalizer{err: finalizeErr}
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{result: frozen},
		&recordingExecutionJobs{},
		finalizer,
		&recordingRunEvidenceAdmitter{},
	)
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
	recordIntentAndJob(run)
	job := recordedJob(t, run, resolvedRunForPrepared(t, prepared), &batchv1.JobStatus{
		Succeeded:  1,
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
	})
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
	want := errors.New("evidence store temporarily unavailable")
	finalizer := &recordingRunEvidenceFinalizer{err: want}
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)},
		&recordingExecutionJobs{},
		finalizer,
		&recordingRunEvidenceAdmitter{},
	)
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
	reconciler, err := NewRunReconciler(
		kubeClient, resolver, jobs,
		&recordingRunEvidenceFinalizer{}, &recordingRunEvidenceAdmitter{},
	)
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
	recordIntentAndJob(run)
	job := recordedJob(t, run, resolvedRunForPrepared(t, prepared), &batchv1.JobStatus{
		Succeeded:  1,
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
	})
	finalized := FinalizedRunEvidence{
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
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)},
		&recordingExecutionJobs{},
		finalizer,
		&recordingRunEvidenceAdmitter{},
	)
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
}

func TestRunReconcilerPublishesScientificAdmissionWithoutNewExecution(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		verdict    profileradmission.Verdict
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{name: "accepted", verdict: profileradmission.VerdictAccepted, wantStatus: metav1.ConditionTrue, wantReason: "EvidenceAccepted"},
		{name: "rejected", verdict: profileradmission.VerdictRejected, wantStatus: metav1.ConditionFalse, wantReason: "EvidenceRejected"},
		{name: "insufficient", verdict: profileradmission.VerdictInsufficient, wantStatus: metav1.ConditionUnknown, wantReason: "EvidenceInsufficient"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scheme := runReconcilerTestScheme(t)
			run, job := admissionReadyRun(t)
			prepared := currentPreparedRun(t)
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
			admitter := &recordingRunEvidenceAdmitter{result: profileradmission.Result{
				Verdict: test.verdict, PolicyVersion: "wall-power-v2",
				ReasonCodes: []string{"ThermalStateUnavailable", "WorkloadWindowUnknown"},
			}}
			jobs := &recordingExecutionJobs{}
			finalizer := &recordingRunEvidenceFinalizer{}
			reconciler, err := NewRunReconciler(
				kubeClient,
				&recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)},
				jobs,
				finalizer,
				admitter,
			)
			if err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
			if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if admitter.calls != 1 || jobs.calls != 0 || finalizer.calls != 0 {
				t.Fatalf("transition calls: admit=%d jobs=%d finalize=%d", admitter.calls, jobs.calls, finalizer.calls)
			}
			actual := &edgev1alpha1.ProfilingRun{}
			if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
				t.Fatal(err)
			}
			if actual.Status.Admission == nil || actual.Status.Admission.Verdict != string(test.verdict) ||
				actual.Status.Admission.PolicyVersion != "wall-power-v2" || len(actual.Status.Admission.ReasonCodes) != 2 {
				t.Fatalf("admission status = %#v", actual.Status.Admission)
			}
			condition := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionEvidenceAdmitted)
			if condition == nil || condition.Status != test.wantStatus || condition.Reason != test.wantReason {
				t.Fatalf("admission condition = %#v", condition)
			}
		})
	}
}

func TestRunReconcilerRetriesAdmissionEvaluationFailure(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run, job := admissionReadyRun(t)
	prepared := currentPreparedRun(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
	want := errors.New("admission evaluator temporarily unavailable")
	admitter := &recordingRunEvidenceAdmitter{err: want}
	jobs := &recordingExecutionJobs{}
	finalizer := &recordingRunEvidenceFinalizer{}
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{result: resolvedRunForPrepared(t, prepared)},
		jobs,
		finalizer,
		admitter,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}

	for attempt := 1; attempt <= 2; attempt++ {
		result, reconcileErr := reconciler.Reconcile(context.Background(), request)
		if !errors.Is(reconcileErr, want) || result != (ctrl.Result{}) {
			t.Fatalf("attempt %d result = %#v, error = %v, want wrapped %v", attempt, result, reconcileErr, want)
		}
		if !strings.Contains(reconcileErr.Error(), "admit ProfilingRun chill-system/run evidence") {
			t.Fatalf("attempt %d error context = %q", attempt, reconcileErr)
		}
		if admitter.calls != attempt || jobs.calls != 0 || finalizer.calls != 0 {
			t.Fatalf(
				"attempt %d transition calls: admit=%d jobs=%d finalize=%d",
				attempt, admitter.calls, jobs.calls, finalizer.calls,
			)
		}
	}

	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	condition := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionEvidenceAdmitted)
	if actual.Status.Admission != nil || condition == nil || condition.Status != metav1.ConditionUnknown ||
		condition.ObservedGeneration != run.Generation || condition.Reason != "AdmissionEvaluationFailed" ||
		condition.Message != "verified evidence could not be evaluated" {
		t.Fatalf("retryable admission failure status = %#v", actual.Status)
	}
}

func TestRunReconcilerJoinsAdmissionAndStatusPublicationFailures(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	statusErr := errors.New("status API temporarily unavailable")
	reconciler := &RunReconciler{Client: &statusPatchFailureClient{Client: baseClient, err: statusErr}}
	admissionErr := errors.New("admission evaluator temporarily unavailable")

	err := reconciler.handleAdmissionFailure(
		context.Background(), types.NamespacedName{Namespace: run.Namespace, Name: run.Name}, run, admissionErr,
	)
	if !errors.Is(err, admissionErr) || !errors.Is(err, statusErr) {
		t.Fatalf("joined admission failure = %v, want %v and %v", err, admissionErr, statusErr)
	}
}

func admissionReadyRun(t *testing.T) (*edgev1alpha1.ProfilingRun, *batchv1.Job) {
	t.Helper()
	run := testRun()
	recordIntentAndJob(run)
	run.Status.Evidence = testEvidenceReference()
	run.Status.ObservedExecution = &edgev1alpha1.ProfilingRunObservedExecutionStatus{
		Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "run-pod", UID: "pod-uid"},
		Node: edgev1alpha1.ProfilingRunObjectReference{Name: "lattepanda", UID: "node-uid"},
	}
	job := recordedJob(t, run, resolvedRunForPrepared(t, currentPreparedRun(t)), &batchv1.JobStatus{
		Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
	})
	return run, job
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
	admissionPolicy, err := resolved.AdmissionPolicy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	executionPolicy, err := resolved.ExecutionPolicy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	job.Annotations = map[string]string{
		admissionPolicyAnnotation: string(admissionPolicy),
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
	reconciler, err := NewRunReconciler(
		kubeClient, resolver, jobs,
		&recordingRunEvidenceFinalizer{}, &recordingRunEvidenceAdmitter{},
	)
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
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	want := errors.New("unresolved execution facts")
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingRunConfigResolver{err: want},
		&recordingExecutionJobs{},
		&recordingRunEvidenceFinalizer{},
		&recordingRunEvidenceAdmitter{},
	)
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
	run.Generation = 7
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	resolver := &recordingRunConfigResolver{
		err: profiler.InvalidAdmissionPolicy("admission policy %q is not configured", "typo-v9"),
	}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(
		kubeClient,
		resolver,
		jobs,
		&recordingRunEvidenceFinalizer{},
		&recordingRunEvidenceAdmitter{},
	)
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
		condition.Reason != profiler.ResolutionReasonAdmissionPolicyInvalid ||
		!strings.Contains(condition.Message, `admission policy "typo-v9" is not configured`) {
		t.Fatalf("terminal freeze status = %#v", actual.Status)
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
	admissionPolicyName := profileradmission.WallPowerPolicyV2
	if prepared.RunConfig.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		admissionPolicyName = profileradmission.IdleWallPowerPolicyV1
	}
	policy := configuredAdmissionPolicy(
		t,
		admissionPolicyName,
		prepared.RunConfig.Measurement.WorkloadMode,
	)
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
		AdmissionPolicy: policy,
		ExecutionPolicy: executionPolicy,
	}
}

func completedTransitionForRun(
	run *edgev1alpha1.ProfilingRun,
	frozen ResolvedRun,
	name string,
	powerMode string,
	readyAt time.Time,
) *edgev1alpha1.NodeActuation {
	intent := frozen.Prepared.RunConfig
	actuator := projectActuatorTarget(intent.Actuation.Actuator)
	if powerMode != intent.Actuation.PowerMode && actuator.JetsonNVPModel != nil {
		actuator.JetsonNVPModel.ID++
	}
	requestedAt := readyAt.Add(-2 * time.Minute)
	targetObservedAt := readyAt.Add(-time.Minute)
	previousObservedAt := requestedAt.Add(10 * time.Second)
	ready := metav1.NewMicroTime(readyAt)
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: run.Namespace, Name: name, UID: types.UID(name + "-uid"), Generation: 1,
		},
		Spec: edgev1alpha1.NodeActuationSpec{
			Node:                        edgev1alpha1.ProfilingRunObjectReference{Name: intent.TargetNode.Name, UID: intent.TargetNode.UID},
			DeviceClass:                 intent.State.DeviceClass,
			PowerMode:                   powerMode,
			Actuator:                    actuator,
			PolicyName:                  intent.Actuation.PolicyName,
			SettlingDurationSeconds:     30,
			MaxActuationDurationSeconds: 300,
			StageRequestedAt:            metav1.NewTime(requestedAt),
			Deadline:                    metav1.NewTime(requestedAt.Add(5 * time.Minute)),
		},
		Status: edgev1alpha1.NodeActuationStatus{
			ObservedGeneration: 1,
			Phase:              edgev1alpha1.NodeActuationPhaseReady,
			PreviousObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: powerMode, Actuator: actuator, ObservedAt: metav1.NewMicroTime(previousObservedAt),
			},
			TargetObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: powerMode, Actuator: actuator, ObservedAt: metav1.NewMicroTime(targetObservedAt),
			},
			ReadyAt: &ready,
		},
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
