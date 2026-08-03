package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
	calls  int
	frozen FrozenRun
	job    *batchv1.Job
	err    error
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

func (a *recordingRunEvidenceAdmitter) Admit(
	context.Context,
	*edgev1alpha1.ProfilingRun,
	FrozenRun,
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
	frozen FrozenRun,
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

func TestRunReconcilerFreezesThenRecordsOneMeasurementJob(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	prepared := currentPreparedRun(t)
	run.Status.IntentDigest = prepared.IntentDigest
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	freezer := &recordingIntentFreezer{result: FreezeIntentResult{Run: frozenRunForPrepared(t, prepared)}}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(
		kubeClient, freezer, jobs, &recordingRunEvidenceFinalizer{}, &recordingRunEvidenceAdmitter{},
	)
	if err != nil {
		t.Fatal(err)
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if freezer.calls != 1 || jobs.calls != 1 || jobs.frozen.Prepared.IntentDigest != prepared.IntentDigest {
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
	reconciler, err := NewRunReconciler(
		kubeClient, freezer, jobs, &recordingRunEvidenceFinalizer{}, &recordingRunEvidenceAdmitter{},
	)
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

func TestRunReconcilerFinalizesSuccessfulJobAndPublishesCompactStatus(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	prepared := currentPreparedRun(t)
	recordIntentAndJob(run, prepared.IntentDigest)
	job := ownedJob(run, &batchv1.JobStatus{
		Succeeded:  1,
		Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
	})
	finalized := FinalizedRunEvidence{
		Reference: evidencestore.ObjectIdentity{
			Store: "chill-evidence-store", Key: "runs/run-uid/execution.json",
			Digest: "sha256:" + strings.Repeat("b", 64), Size: 1024,
		},
		Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "run-pod", UID: "pod-uid"},
		Node: edgev1alpha1.ProfilingRunObjectReference{Name: "lattepanda", UID: "node-uid"},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
	finalizer := &recordingRunEvidenceFinalizer{result: finalized}
	reconciler, err := NewRunReconciler(
		kubeClient,
		&recordingIntentFreezer{result: FreezeIntentResult{Run: frozenRunForPrepared(t, prepared)}},
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
	if actual.Status.Evidence == nil || actual.Status.Evidence.Digest != finalized.Reference.Digest ||
		actual.Status.Evidence.SchemaVersion != profilerevidence.ExecutionEvidenceSchemaVersion ||
		actual.Status.ActualExecution == nil || actual.Status.ActualExecution.Pod != finalized.Pod ||
		actual.Status.ActualExecution.Node != finalized.Node {
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
			run := testRun()
			prepared := currentPreparedRun(t)
			recordIntentAndJob(run, prepared.IntentDigest)
			run.Status.Evidence = testEvidenceReference()
			run.Status.ActualExecution = &edgev1alpha1.ProfilingRunActualExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "run-pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "lattepanda", UID: "node-uid"},
			}
			job := ownedJob(run, &batchv1.JobStatus{
				Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
			})
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run, job).Build()
			admitter := &recordingRunEvidenceAdmitter{result: profileradmission.Result{
				Verdict: test.verdict, PolicyVersion: "wall-power-v2",
				ReasonCodes: []string{"ThermalStateUnavailable", "WorkloadWindowUnknown"},
			}}
			jobs := &recordingExecutionJobs{}
			finalizer := &recordingRunEvidenceFinalizer{}
			reconciler, err := NewRunReconciler(
				kubeClient,
				&recordingIntentFreezer{result: FreezeIntentResult{Run: frozenRunForPrepared(t, prepared)}},
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

func TestRunReconcilerIgnoresMissingRun(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	freezer := &recordingIntentFreezer{}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(
		kubeClient, freezer, jobs, &recordingRunEvidenceFinalizer{}, &recordingRunEvidenceAdmitter{},
	)
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

func TestRunReconcilerPublishesTerminalFreezeFailureWithoutRequeue(t *testing.T) {
	t.Parallel()
	scheme := runReconcilerTestScheme(t)
	run := testRun()
	run.Generation = 7
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(run).WithObjects(run).Build()
	freezer := &recordingIntentFreezer{
		err: invalidAdmissionPolicy("admission policy %q is not configured", "typo-v9"),
	}
	jobs := &recordingExecutionJobs{}
	reconciler, err := NewRunReconciler(
		kubeClient,
		freezer,
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
	if freezer.calls != 1 || jobs.calls != 0 {
		t.Fatalf("terminal freeze calls: freezer=%d jobs=%d", freezer.calls, jobs.calls)
	}
	actual := &edgev1alpha1.ProfilingRun{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	condition := findRunCondition(actual.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
	if actual.Status.ObservedGeneration != run.Generation || condition == nil ||
		condition.Status != metav1.ConditionFalse || condition.ObservedGeneration != run.Generation ||
		condition.Reason != freezeReasonAdmissionPolicyInvalid ||
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

func frozenRunForPrepared(t *testing.T, prepared profiler.PreparedRun) FrozenRun {
	t.Helper()
	admissionPolicyName := profileradmission.WallPowerPolicyV2
	if prepared.Intent.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		admissionPolicyName = profileradmission.IdleWallPowerPolicyV1
	}
	policy, err := profileradmission.LegacyFrozenPolicy(
		admissionPolicyName,
		prepared.Intent.Measurement.WorkloadMode,
	)
	if err != nil {
		t.Fatal(err)
	}
	executionPolicy, err := profiler.LegacyFrozenExecutionPolicy(prepared.Intent)
	if err != nil {
		t.Fatal(err)
	}
	return FrozenRun{
		Prepared:        prepared,
		AdmissionPolicy: policy,
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
