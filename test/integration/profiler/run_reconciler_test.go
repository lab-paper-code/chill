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
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
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
	"sigs.k8s.io/yaml"
)

const profilerIntegrationNamespace = "profiler-integration"

const profilerIntegrationDeletionHold = "test.chill.dacs.io/hold-deletion"

type staticRunConfigResolver struct {
	resolved profilercontroller.ResolvedRun
	calls    int
}

func (r *staticRunConfigResolver) Resolve(
	_ context.Context,
	_ *edgev1alpha1.ProfilingRun,
) (profilercontroller.ResolvedRun, error) {
	r.calls++
	return r.resolved, nil
}

type apiExecutionJobs struct {
	kube     client.Client
	resolved profilercontroller.ResolvedRun
	calls    int
	creates  int
}

func (j *apiExecutionJobs) Find(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (*batchv1.Job, *profilercontroller.ResolvedRun, error) {
	key := client.ObjectKey{Namespace: run.Namespace, Name: run.Name + "-measurement"}
	job := &batchv1.Job{}
	if err := j.kube.Get(ctx, key, job); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	resolved := j.resolved
	return job, &resolved, nil
}

func (j *apiExecutionJobs) Ensure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	_ profilercontroller.ResolvedRun,
) (*batchv1.Job, bool, error) {
	j.calls++
	key := client.ObjectKey{Namespace: run.Namespace, Name: run.Name + "-measurement"}
	existing := &batchv1.Job{}
	if err := j.kube.Get(ctx, key, existing); err == nil {
		return existing, false, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, false, err
	}
	runConfig, err := json.Marshal(j.resolved.Prepared.RunConfig)
	if err != nil {
		return nil, false, err
	}
	executionPolicy, err := j.resolved.ExecutionPolicy.CanonicalJSON()
	if err != nil {
		return nil, false, err
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: key.Namespace, Name: key.Name,
			Annotations: map[string]string{
				"edge.dacs.io/profiling-execution-policy": string(executionPolicy),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr.To(int32(0)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name: "runtime", Image: "example.invalid/chill/runtime:test",
					Args: []string{"--run-config=" + string(runConfig)},
				}},
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

type reconcilerHarness struct {
	reconciler *profilercontroller.RunReconciler
	resolver   *staticRunConfigResolver
	jobs       *apiExecutionJobs
	finalizer  *recordingFinalizer
}

func TestRunReconcilerPersistsJobIdentityAcrossDuplicatesRestartAndLoss(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	run, resolved := createRunWithResolvedConfig(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	initial := newReconcilerHarness(t, kube, resolved)
	reconcile(t, ctx, initial.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)
	requireMaterializationCount(t, initial.jobs, 1, 1, "initial reconcile")

	reconcile(t, ctx, initial.reconciler, request)
	requireMaterializationCount(t, initial.jobs, 1, 1, "duplicate reconcile")
	assertSingleOwnedJob(t, ctx, kube, afterCreate.UID, recordedJob)

	restarted := newReconcilerHarness(t, kube, resolved)
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

func TestRunReconcilerIsCompleteAfterFinalizationRestart(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	run, resolved := createRunWithResolvedConfig(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	finalizing := newReconcilerHarness(t, kube, resolved)
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
	if finalizing.finalizer.calls != 1 {
		t.Fatalf("finalization calls: %d", finalizing.finalizer.calls)
	}
	restarted := newReconcilerHarness(t, kube, resolved)
	for i := 0; i < 2; i++ {
		reconcile(t, ctx, restarted.reconciler, request)
		completed := getRun(t, ctx, kube, request.NamespacedName)
		assertFinalizedEvidence(t, completed, finalizing.finalizer.result)
		if completed.ResourceVersion != afterFinalization.ResourceVersion {
			t.Fatal("completed Run rewrote status")
		}
	}
	if restarted.finalizer.calls != 0 {
		t.Fatal("completed Run finalized again")
	}
}

func TestRunReconcilerRejectsReplacementJobIdentityDrift(t *testing.T) {
	ctx, kube := startProfilerEnvtest(t)
	run, resolved := createRunWithResolvedConfig(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	initial := newReconcilerHarness(t, kube, resolved)
	reconcile(t, ctx, initial.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)

	deleteJobAndWait(t, ctx, kube, run.Namespace, recordedJob.Name)
	replacement := createReplacementJob(t, ctx, kube, run.Namespace, recordedJob.Name)
	if string(replacement.UID) == recordedJob.UID {
		t.Fatalf("replacement Job unexpectedly retained recorded UID %s", recordedJob.UID)
	}

	restarted := newReconcilerHarness(t, kube, resolved)
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
	run, resolved := createRunWithResolvedConfig(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	initial := newReconcilerHarness(t, kube, resolved)
	reconcile(t, ctx, initial.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)
	changeJobControllerUID(t, ctx, kube, run.Namespace, recordedJob.Name, "foreign-run-uid")

	restarted := newReconcilerHarness(t, kube, resolved)
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
	run, resolved := createRunWithResolvedConfig(t, ctx, kube)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}

	initial := newReconcilerHarness(t, kube, resolved)
	reconcile(t, ctx, initial.reconciler, request)
	afterCreate := getRun(t, ctx, kube, request.NamespacedName)
	recordedJob := requireRecordedJob(t, afterCreate)
	deleting := markRunDeleting(t, ctx, kube, afterCreate)

	restarted := newReconcilerHarness(t, kube, resolved)
	reconcile(t, ctx, restarted.reconciler, request)
	afterStop := getRun(t, ctx, kube, request.NamespacedName)
	if afterStop.ResourceVersion != deleting.ResourceVersion {
		t.Fatalf(
			"deletion fence rewrote Run: before=%s after=%s",
			deleting.ResourceVersion,
			afterStop.ResourceVersion,
		)
	}
	if restarted.resolver.calls != 0 {
		t.Fatalf("deletion fence resolver calls = %d, want 0", restarted.resolver.calls)
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

func createRunWithResolvedConfig(
	t *testing.T,
	ctx context.Context,
	kube client.Client,
) (*edgev1alpha1.ProfilingRun, profilercontroller.ResolvedRun) {
	t.Helper()
	runConfig := loadRunConfig(t)
	run := validRun()
	if err := kube.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
		t.Fatal(err)
	}
	return run, resolvedRun(t, profiler.PreparedRun{RunConfig: runConfig})
}

func newReconcilerHarness(
	t *testing.T,
	kube client.Client,
	resolved profilercontroller.ResolvedRun,
) *reconcilerHarness {
	t.Helper()
	resolver := &staticRunConfigResolver{resolved: resolved}
	jobs := &apiExecutionJobs{kube: kube, resolved: resolved}
	finalizer := &recordingFinalizer{err: errors.New("unexpected finalization")}
	reconciler := newRunReconciler(t, kube, resolver, jobs, finalizer)
	return &reconcilerHarness{
		reconciler: reconciler, resolver: resolver, jobs: jobs,
		finalizer: finalizer,
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
	if harness.jobs.calls != 0 || harness.finalizer.calls != 0 {
		t.Fatalf(
			"loss path crossed execution boundaries: jobs=%d finalize=%d",
			harness.jobs.calls, harness.finalizer.calls,
		)
	}
}

func newRunReconciler(
	t *testing.T,
	kube client.Client,
	resolver *staticRunConfigResolver,
	jobs *apiExecutionJobs,
	finalizer *recordingFinalizer,
) *profilercontroller.RunReconciler {
	t.Helper()
	reconciler, err := profilercontroller.NewRunReconciler(
		kube, resolver, jobs, finalizer,
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
		run.Status.ObservedExecution == nil || run.Status.ObservedExecution.Pod != finalized.Pod ||
		run.Status.ObservedExecution.Node != finalized.Node {
		t.Fatalf("finalized evidence status is incomplete: %#v", run.Status)
	}
	evidence := findCondition(run.Status.Conditions, edgev1alpha1.ProfilingRunConditionEvidenceVerified)
	if evidence == nil || evidence.Status != metav1.ConditionTrue || evidence.Reason != "EvidenceVerified" {
		t.Fatalf("finalized evidence condition is incomplete: %#v", run.Status.Conditions)
	}
}

func loadRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "internal", "profiling", "profiler", "controller", "testdata", "lattepanda-ort-cpu-bs1.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	return config
}

func resolvedRun(
	t *testing.T,
	prepared profiler.PreparedRun,
) profilercontroller.ResolvedRun {
	t.Helper()
	source := profiler.ExecutionPolicySource{
		Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: "chill-system",
		Name: "chill-profiler-facts", UID: "facts-uid", ResourceVersion: "42", Key: "execution-policies.yaml",
	}
	const mebibyte = int64(1024 * 1024)
	executionCatalog, err := yaml.Marshal(profiler.ExecutionPolicyCatalog{
		SchemaVersion: profiler.ExecutionPolicyCatalogSchemaVersion,
		Policies: []profiler.ExecutionPolicySpec{{
			Name: "integration-active", DeviceClass: "*",
			ExecutionPath:                 prepared.RunConfig.Candidate.ExecutionPath,
			WorkloadMode:                  prepared.RunConfig.Measurement.WorkloadMode,
			ActiveDeadlineOverheadSeconds: 180, AttemptTimeoutOverheadSeconds: 150,
			ObserverReadyTimeoutSeconds: 30, ObserverStartOverheadSeconds: 120,
			MetricResultOverheadSeconds: 30, WorkloadEndFenceTimeoutSeconds: 15,
			WorkloadResultTimeoutSeconds: 30, MaxLatencySamples: 1_000_000,
			MaxInputBytes: 64 * mebibyte, MaxRuntimeResultBytes: 32 * mebibyte,
			MaxMetricResultBytes: 8 * mebibyte, MaxMetricEvents: 50_000,
			MaxWorkloadEvents: 10_000, MaxWorkloadResultBytes: 8 * mebibyte,
			RuntimeMemoryRequestBytes: 128 * mebibyte,
			RuntimeMemoryLimitBytes:   1024 * mebibyte,
			ObserverCPURequest:        "5m", ObserverCPULimit: "100m",
			ObserverMemoryRequestBytes: 16 * mebibyte,
			ObserverMemoryLimitBytes:   64 * mebibyte,
			WorkloadObserverCPURequest: "5m", WorkloadObserverCPULimit: "100m",
			WorkloadObserverMemoryRequestBytes: 32 * mebibyte,
			WorkloadObserverMemoryLimitBytes:   128 * mebibyte,
			CoordinationReserveBytes:           mebibyte, WorkspaceBytes: 128 * mebibyte,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	executionPolicy, err := profiler.FreezeConfiguredExecutionPolicy(
		executionCatalog,
		prepared.RunConfig,
		profiler.ExecutionPolicySource{
			Kind: profiler.ConfigMapExecutionPolicySourceKind, Namespace: source.Namespace,
			Name: source.Name, UID: source.UID, ResourceVersion: source.ResourceVersion,
			Key: "execution-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return profilercontroller.ResolvedRun{
		Prepared: prepared, ExecutionPolicy: executionPolicy,
	}
}

func validRun() *edgev1alpha1.ProfilingRun {
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Name: "durable-job-identity", Namespace: profilerIntegrationNamespace,
			Annotations: map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Now())}},
		Spec: edgev1alpha1.ProfilingRunSpec{
			MeasurementStateSpec: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec: "mobilenet-v2-050", DeviceClass: "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu", PowerMode: "fixed", Preprocessor: "mobilenet-imagenet",
				},
				TargetNode: "lattepanda",
				CPURequest: resource.MustParse("100m"),
			},
			Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{
				Batch: 1, WarmupIterations: 20, DurationSeconds: 30,
				WorkloadMode: "saturated-serial-fixed-batch", Dataset: "vqav2-val-subset",
			},
			MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
				IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
			},
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
