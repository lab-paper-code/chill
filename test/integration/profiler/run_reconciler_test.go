package profilerintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const profilerIntegrationNamespace = "profiler-integration"

const profilerIntegrationDeletionHold = "test.chill.dacs.io/hold-deletion"

type staticIntentFreezer struct {
	prepared profiler.PreparedRun
	calls    int
}

func (f *staticIntentFreezer) Freeze(
	_ context.Context,
	run *edgev1alpha1.ProfilingRun,
) (profilercontroller.FreezeIntentResult, error) {
	f.calls++
	if !run.DeletionTimestamp.IsZero() {
		return profilercontroller.FreezeIntentResult{Stopped: true}, nil
	}
	return profilercontroller.FreezeIntentResult{
		Run: profilercontroller.FrozenRun{Prepared: f.prepared},
	}, nil
}

type apiExecutionJobs struct {
	kube    client.Client
	calls   int
	creates int
}

func (j *apiExecutionJobs) Ensure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	_ profilercontroller.FrozenRun,
) (*batchv1.Job, bool, error) {
	j.calls++
	key := client.ObjectKey{Namespace: run.Namespace, Name: run.Name + "-measurement"}
	existing := &batchv1.Job{}
	if err := j.kube.Get(ctx, key, existing); err == nil {
		return existing, false, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, false, err
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(0)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers:    []corev1.Container{{Name: "runtime", Image: "example.invalid/chill/runtime:test"}},
			}},
		},
	}
	if err := controllerutil.SetControllerReference(run, job, clientgoscheme.Scheme); err != nil {
		return nil, false, err
	}
	if err := j.kube.Create(ctx, job); err != nil {
		return nil, false, err
	}
	j.creates++
	return job, true, nil
}

type recordingFinalizer struct {
	calls  int
	result profilercontroller.FinalizedRunEvidence
	err    error
}

func (f *recordingFinalizer) Finalize(
	context.Context,
	*edgev1alpha1.ProfilingRun,
	profiler.PreparedRun,
	*batchv1.Job,
) (profilercontroller.FinalizedRunEvidence, error) {
	f.calls++
	return f.result, f.err
}

type recordingAdmitter struct {
	calls  int
	result profileradmission.Result
	err    error
}

func (a *recordingAdmitter) Admit(
	context.Context,
	*edgev1alpha1.ProfilingRun,
	profilercontroller.FrozenRun,
	*batchv1.Job,
	evidencestore.ObjectIdentity,
) (profileradmission.Result, error) {
	a.calls++
	return a.result, a.err
}

type reconcilerHarness struct {
	reconciler *profilercontroller.RunReconciler
	freezer    *staticIntentFreezer
	jobs       *apiExecutionJobs
	finalizer  *recordingFinalizer
	admitter   *recordingAdmitter
}

func TestRunReconcilerPersistsJobIdentityAcrossDuplicatesRestartAndLoss(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	run, prepared := createRunWithFrozenIntent(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	initial := newReconcilerHarness(t, kube, prepared)
	reconcile(t, ctx, initial.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)
	requireMaterializationCount(t, initial.jobs, 1, 1, "initial reconcile")

	reconcile(t, ctx, initial.reconciler, request)
	requireMaterializationCount(t, initial.jobs, 1, 1, "duplicate reconcile")
	assertSingleOwnedJob(t, ctx, kube, afterCreate.UID, recordedJob)

	restarted := newReconcilerHarness(t, kube, prepared)
	reconcile(t, ctx, restarted.reconciler, request)
	requireMaterializationCount(t, restarted.jobs, 0, 0, "restarted reconciler")
	assertSingleOwnedJob(t, ctx, kube, afterCreate.UID, recordedJob)

	deleteJobAndWait(t, ctx, kube, run.Namespace, recordedJob.Name)
	reconcile(t, ctx, restarted.reconciler, request)
	afterLoss := getRun(t, ctx, kube, request.NamespacedName)
	assertExecutionLost(t, afterLoss, recordedJob)
	requireMaterializationCount(t, restarted.jobs, 0, 0, "recorded Job loss")

	resourceVersion := afterLoss.ResourceVersion
	reconcile(t, ctx, restarted.reconciler, request)
	afterDuplicateLoss := getRun(t, ctx, kube, request.NamespacedName)
	if afterDuplicateLoss.ResourceVersion != resourceVersion {
		t.Fatalf(
			"duplicate loss event rewrote stable status: before=%s after=%s",
			resourceVersion,
			afterDuplicateLoss.ResourceVersion,
		)
	}
	assertNoExecutionCalls(t, restarted)
}

func TestRunReconcilerResumesAdmissionAfterFinalizationRestart(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	run, prepared := createRunWithFrozenIntent(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	finalizing := newReconcilerHarness(t, kube, prepared)
	reconcile(t, ctx, finalizing.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)
	markJobSucceeded(t, ctx, kube, run.Namespace, recordedJob.Name)
	finalizing.finalizer.err = nil
	finalizing.finalizer.result = profilercontroller.FinalizedRunEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Reference: evidencestore.ObjectIdentity{
			Store: "chill-evidence-store", Key: "runs/durable-job-identity/execution.json",
			Size: 1024,
		},
		Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "measurement-pod", UID: "pod-uid"},
		Node: edgev1alpha1.ProfilingRunObjectReference{Name: "lattepanda", UID: "node-uid"},
	}

	reconcile(t, ctx, finalizing.reconciler, request)
	afterFinalization := getRun(t, ctx, kube, request.NamespacedName)
	assertFinalizedEvidence(t, afterFinalization, finalizing.finalizer.result)
	if finalizing.finalizer.calls != 1 || finalizing.admitter.calls != 0 {
		t.Fatalf(
			"finalization transition calls: finalize=%d admit=%d",
			finalizing.finalizer.calls,
			finalizing.admitter.calls,
		)
	}

	restarted := newReconcilerHarness(t, kube, prepared)
	restarted.admitter.err = nil
	restarted.admitter.result = profileradmission.Result{
		Verdict: profileradmission.VerdictAccepted, PolicyVersion: "wall-power-v2",
	}
	reconcile(t, ctx, restarted.reconciler, request)
	afterAdmission := getRun(t, ctx, kube, request.NamespacedName)
	assertAcceptedAdmission(t, afterAdmission, restarted.admitter.result.PolicyVersion)
	if restarted.finalizer.calls != 0 || restarted.admitter.calls != 1 {
		t.Fatalf(
			"restart transition calls: finalize=%d admit=%d",
			restarted.finalizer.calls,
			restarted.admitter.calls,
		)
	}

	resourceVersion := afterAdmission.ResourceVersion
	reconcile(t, ctx, restarted.reconciler, request)
	afterDuplicate := getRun(t, ctx, kube, request.NamespacedName)
	if afterDuplicate.ResourceVersion != resourceVersion {
		t.Fatalf(
			"completed duplicate event rewrote status: before=%s after=%s",
			resourceVersion,
			afterDuplicate.ResourceVersion,
		)
	}
	if restarted.finalizer.calls != 0 || restarted.admitter.calls != 1 {
		t.Fatalf(
			"completed duplicate crossed boundaries: finalize=%d admit=%d",
			restarted.finalizer.calls,
			restarted.admitter.calls,
		)
	}
}

func TestRunReconcilerRejectsReplacementJobIdentityDrift(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	run, prepared := createRunWithFrozenIntent(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	initial := newReconcilerHarness(t, kube, prepared)
	reconcile(t, ctx, initial.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)

	deleteJobAndWait(t, ctx, kube, run.Namespace, recordedJob.Name)
	replacement := createReplacementJob(t, ctx, kube, run.Namespace, recordedJob.Name)
	if string(replacement.UID) == recordedJob.UID {
		t.Fatalf("replacement Job unexpectedly retained recorded UID %s", recordedJob.UID)
	}

	restarted := newReconcilerHarness(t, kube, prepared)
	_, err := restarted.reconciler.Reconcile(ctx, request)
	if err == nil || !strings.Contains(err.Error(), "observed Job identity does not match recorded reference") {
		t.Fatalf("replacement Job reconcile error = %v, want recorded identity mismatch", err)
	}
	afterDrift := getRun(t, ctx, kube, request.NamespacedName)
	assertRunUnchangedAfterDrift(t, afterDrift, afterCreate.ResourceVersion, recordedJob)
	assertNoExecutionCalls(t, restarted)
}

func TestRunReconcilerRejectsRecordedJobOwnerDrift(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	run, prepared := createRunWithFrozenIntent(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	initial := newReconcilerHarness(t, kube, prepared)
	reconcile(t, ctx, initial.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)
	changeJobControllerUID(t, ctx, kube, run.Namespace, recordedJob.Name, "foreign-run-uid")

	restarted := newReconcilerHarness(t, kube, prepared)
	_, err := restarted.reconciler.Reconcile(ctx, request)
	if err == nil || !strings.Contains(err.Error(), "observed Job is not controlled by ProfilingRun") {
		t.Fatalf("owner-drift reconcile error = %v, want controller-owner mismatch", err)
	}
	afterDrift := getRun(t, ctx, kube, request.NamespacedName)
	assertRunUnchangedAfterDrift(t, afterDrift, afterCreate.ResourceVersion, recordedJob)
	assertNoExecutionCalls(t, restarted)
}

func TestRunReconcilerStopsAtDeletionFence(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	run, prepared := createRunWithFrozenIntent(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	initial := newReconcilerHarness(t, kube, prepared)
	reconcile(t, ctx, initial.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)
	deleting := markRunDeleting(t, ctx, kube, afterCreate)

	restarted := newReconcilerHarness(t, kube, prepared)
	reconcile(t, ctx, restarted.reconciler, request)
	afterStop := getRun(t, ctx, kube, request.NamespacedName)
	if afterStop.ResourceVersion != deleting.ResourceVersion {
		t.Fatalf(
			"deletion fence rewrote Run: before=%s after=%s",
			deleting.ResourceVersion,
			afterStop.ResourceVersion,
		)
	}
	if restarted.freezer.calls != 1 {
		t.Fatalf("deletion fence freezer calls = %d, want 1", restarted.freezer.calls)
	}
	assertNoExecutionCalls(t, restarted)
	assertSingleOwnedJob(t, ctx, kube, afterStop.UID, recordedJob)
}

func startProfilerEnvtest(t *testing.T) (context.Context, client.Client) {
	t.Helper()
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: filepath.Join(
			"..", "..", "..", "bin", "k8s", fmt.Sprintf("1.31.0-%s-%s", runtime.GOOS, runtime.GOARCH),
		),
	}
	config, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	if err := edgev1alpha1.AddToScheme(clientgoscheme.Scheme); err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(config, client.Options{Scheme: clientgoscheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: profilerIntegrationNamespace}}
	if err := kube.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	return ctx, kube
}

func createRunWithFrozenIntent(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
) (*edgev1alpha1.ProfilingRun, profiler.PreparedRun) {
	t.Helper()
	intent := loadRetainedIntent(t)
	run := validRun()
	if err := kube.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
		t.Fatal(err)
	}
	run.Status.IntentFrozen = true
	run.Status.ObservedGeneration = run.Generation
	if err := kube.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	return run, profiler.PreparedRun{Intent: intent}
}

func newReconcilerHarness(
	t *testing.T,
	kube client.Client,
	prepared profiler.PreparedRun,
) *reconcilerHarness {
	t.Helper()
	freezer := &staticIntentFreezer{prepared: prepared}
	jobs := &apiExecutionJobs{kube: kube}
	finalizer := &recordingFinalizer{err: errors.New("unexpected finalization")}
	admitter := &recordingAdmitter{err: errors.New("unexpected admission")}
	reconciler := newRunReconciler(t, kube, freezer, jobs, finalizer, admitter)
	return &reconcilerHarness{
		reconciler: reconciler, freezer: freezer, jobs: jobs,
		finalizer: finalizer, admitter: admitter,
	}
}

func deleteJobAndWait(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	namespace, name string,
) {
	t.Helper()
	job := &batchv1.Job{}
	jobKey := client.ObjectKey{Namespace: namespace, Name: name}
	if err := kube.Get(ctx, jobKey, job); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
		t.Fatal(err)
	}
	deleted := func(ctx context.Context) (bool, error) {
		err := kube.Get(ctx, jobKey, &batchv1.Job{})
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}
	if err := wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, 5*time.Second, true, deleted); err != nil {
		t.Fatal(err)
	}
}

func createReplacementJob(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	namespace, name string,
) *batchv1.Job {
	t.Helper()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(0)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name: "foreign", Image: "example.invalid/foreign:test",
				}},
			}},
		},
	}
	if err := kube.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	return job
}

func changeJobControllerUID(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	namespace, name string,
	uid types.UID,
) {
	t.Helper()
	job := &batchv1.Job{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, job); err != nil {
		t.Fatal(err)
	}
	changed := false
	for index := range job.OwnerReferences {
		if ptr.Deref(job.OwnerReferences[index].Controller, false) {
			job.OwnerReferences[index].UID = uid
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("recorded Job has no controller owner to mutate")
	}
	if err := kube.Update(ctx, job); err != nil {
		t.Fatal(err)
	}
}

func markRunDeleting(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	run *edgev1alpha1.ProfilingRun,
) *edgev1alpha1.ProfilingRun {
	t.Helper()
	run.Finalizers = append(run.Finalizers, profilerIntegrationDeletionHold)
	if err := kube.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(ctx, run); err != nil {
		t.Fatal(err)
	}
	deleting := getRun(t, ctx, kube, client.ObjectKeyFromObject(run))
	if deleting.DeletionTimestamp.IsZero() {
		t.Fatal("ProfilingRun deletion timestamp was not persisted")
	}
	t.Cleanup(func() {
		current := &edgev1alpha1.ProfilingRun{}
		if err := kube.Get(context.Background(), client.ObjectKeyFromObject(run), current); err != nil {
			return
		}
		controllerutil.RemoveFinalizer(current, profilerIntegrationDeletionHold)
		_ = kube.Update(context.Background(), current)
	})
	return deleting
}

func reconcile(
	t *testing.T,
	ctx context.Context,
	reconciler *profilercontroller.RunReconciler,
	request ctrl.Request,
) {
	t.Helper()
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
}

func requireRecordedJob(
	t *testing.T,
	run *edgev1alpha1.ProfilingRun,
) edgev1alpha1.ProfilingRunObjectReference {
	t.Helper()
	if run.Status.Job == nil || run.Status.Job.Name == "" || run.Status.Job.UID == "" {
		t.Fatalf("recorded Job identity is incomplete: %#v", run.Status.Job)
	}
	return *run.Status.Job
}

func requireMaterializationCount(
	t *testing.T,
	jobs *apiExecutionJobs,
	wantCalls, wantCreates int,
	phase string,
) {
	t.Helper()
	if jobs.calls != wantCalls || jobs.creates != wantCreates {
		t.Fatalf(
			"%s materialization calls=%d creates=%d, want %d/%d",
			phase, jobs.calls, jobs.creates, wantCalls, wantCreates,
		)
	}
}

func assertExecutionLost(
	t *testing.T,
	run *edgev1alpha1.ProfilingRun,
	recordedJob edgev1alpha1.ProfilingRunObjectReference,
) {
	t.Helper()
	if run.Status.Job == nil || *run.Status.Job != recordedJob {
		t.Fatalf("recorded Job identity changed after loss: %#v", run.Status.Job)
	}
	condition := findCondition(run.Status.Conditions, edgev1alpha1.ProfilingRunConditionExecutionComplete)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "ExecutionLost" {
		t.Fatalf("missing typed ExecutionLost condition: %#v", run.Status.Conditions)
	}
}

func assertRunUnchangedAfterDrift(
	t *testing.T,
	run *edgev1alpha1.ProfilingRun,
	resourceVersion string,
	recordedJob edgev1alpha1.ProfilingRunObjectReference,
) {
	t.Helper()
	if run.ResourceVersion != resourceVersion {
		t.Fatalf(
			"identity drift rewrote Run status: before=%s after=%s",
			resourceVersion,
			run.ResourceVersion,
		)
	}
	if run.Status.Job == nil || *run.Status.Job != recordedJob {
		t.Fatalf("identity drift changed recorded Job reference: %#v", run.Status.Job)
	}
	if condition := findCondition(
		run.Status.Conditions,
		edgev1alpha1.ProfilingRunConditionExecutionComplete,
	); condition != nil {
		t.Fatalf("identity drift was misreported as an execution outcome: %#v", condition)
	}
}

func assertNoExecutionCalls(t *testing.T, harness *reconcilerHarness) {
	t.Helper()
	if harness.jobs.calls != 0 || harness.finalizer.calls != 0 || harness.admitter.calls != 0 {
		t.Fatalf(
			"loss path crossed execution boundaries: jobs=%d finalize=%d admit=%d",
			harness.jobs.calls, harness.finalizer.calls, harness.admitter.calls,
		)
	}
}

func newRunReconciler(
	t *testing.T,
	kube client.Client,
	freezer *staticIntentFreezer,
	jobs *apiExecutionJobs,
	finalizer *recordingFinalizer,
	admitter *recordingAdmitter,
) *profilercontroller.RunReconciler {
	t.Helper()
	handoffs, err := profilercontroller.NewActuationHandoffs(kube, clientgoscheme.Scheme)
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := profilercontroller.NewRunReconciler(
		kube, freezer, handoffs, jobs, finalizer, admitter,
	)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func markJobSucceeded(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	namespace, name string,
) {
	t.Helper()
	job := &batchv1.Job{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, job); err != nil {
		t.Fatal(err)
	}
	job.Status.Succeeded = 1
	job.Status.Conditions = []batchv1.JobCondition{{
		Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
		Reason: "Completed", LastTransitionTime: metav1.Now(),
	}}
	if err := kube.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
}

func assertFinalizedEvidence(
	t *testing.T,
	run *edgev1alpha1.ProfilingRun,
	finalized profilercontroller.FinalizedRunEvidence,
) {
	t.Helper()
	if run.Status.Evidence == nil || run.Status.Evidence.Key != finalized.Reference.Key ||
		run.Status.ActualExecution == nil || run.Status.ActualExecution.Pod != finalized.Pod ||
		run.Status.ActualExecution.Node != finalized.Node {
		t.Fatalf("finalized evidence status is incomplete: %#v", run.Status)
	}
	evidence := findCondition(run.Status.Conditions, edgev1alpha1.ProfilingRunConditionEvidenceVerified)
	if evidence == nil || evidence.Status != metav1.ConditionTrue || evidence.Reason != "EvidenceVerified" {
		t.Fatalf("finalized evidence condition is incomplete: %#v", run.Status.Conditions)
	}
}

func assertAcceptedAdmission(t *testing.T, run *edgev1alpha1.ProfilingRun, policyVersion string) {
	t.Helper()
	if run.Status.Admission == nil || run.Status.Admission.Verdict != "Accepted" ||
		run.Status.Admission.PolicyVersion != policyVersion {
		t.Fatalf("accepted admission status is incomplete: %#v", run.Status.Admission)
	}
	admitted := findCondition(run.Status.Conditions, edgev1alpha1.ProfilingRunConditionEvidenceAdmitted)
	if admitted == nil || admitted.Status != metav1.ConditionTrue || admitted.Reason != "EvidenceAccepted" {
		t.Fatalf("accepted admission condition is incomplete: %#v", run.Status.Conditions)
	}
}

func loadRetainedIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "internal", "profiler", "controller", "testdata", "lattepanda-ort-cpu-bs1.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	var intent profilingrun.Intent
	if err := json.Unmarshal(payload, &intent); err != nil {
		t.Fatal(err)
	}
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func validRun() *edgev1alpha1.ProfilingRun {
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Name: "durable-job-identity", Namespace: profilerIntegrationNamespace},
		Spec: edgev1alpha1.ProfilingRunSpec{
			Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
				ModelSpec: "mobilenet-v2-050", DeviceClass: "lattepanda-3-delta-8g",
				ExecutionPath: "ort-cpu", PowerMode: "fixed",
			},
			TargetNode: "lattepanda", CPURequest: resource.MustParse("100m"),
			Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{
				Batch: 1, WarmupIterations: 20, DurationSeconds: 30,
				WorkloadMode: "saturated-serial-fixed-batch", InputGenerator: "numpy-default-rng-seed-0-fp32-v1",
				OutputValidation: "nonempty-shape-batch-v1",
			},
			MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
				IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
			},
			AdmissionPolicy: "wall-power-v2",
		},
	}
}

func getRun(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	key types.NamespacedName,
) *edgev1alpha1.ProfilingRun {
	t.Helper()
	run := &edgev1alpha1.ProfilingRun{}
	if err := kube.Get(ctx, key, run); err != nil {
		t.Fatal(err)
	}
	return run
}

func assertSingleOwnedJob(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
	runUID types.UID,
	recorded edgev1alpha1.ProfilingRunObjectReference,
) {
	t.Helper()
	jobs := &batchv1.JobList{}
	if err := kube.List(ctx, jobs, client.InNamespace(profilerIntegrationNamespace)); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 1 || jobs.Items[0].Name != recorded.Name || string(jobs.Items[0].UID) != recorded.UID {
		t.Fatalf("Jobs do not match the one recorded identity: %#v", jobs.Items)
	}
	owner := metav1.GetControllerOf(&jobs.Items[0])
	if owner == nil || owner.UID != runUID {
		t.Fatalf("Job controller owner = %#v, want Run UID %s", owner, runUID)
	}
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for index := range conditions {
		if conditions[index].Type == conditionType {
			return &conditions[index]
		}
	}
	return nil
}
