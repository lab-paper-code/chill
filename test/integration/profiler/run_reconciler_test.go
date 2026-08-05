package profilerintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiler/controller"
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

type staticIntentFreezer struct {
	prepared profiler.PreparedRun
	calls    int
}

func (f *staticIntentFreezer) Freeze(
	_ context.Context,
	_ *edgev1alpha1.ProfilingRun,
) (profilercontroller.FreezeIntentResult, error) {
	f.calls++
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

type rejectingFinalizer struct{ calls int }

func (f *rejectingFinalizer) Finalize(
	context.Context,
	*edgev1alpha1.ProfilingRun,
	profiler.PreparedRun,
	*batchv1.Job,
) (profilercontroller.FinalizedRunEvidence, error) {
	f.calls++
	return profilercontroller.FinalizedRunEvidence{}, errors.New("unexpected finalization")
}

type rejectingAdmitter struct{ calls int }

func (a *rejectingAdmitter) Admit(
	context.Context,
	*edgev1alpha1.ProfilingRun,
	profilercontroller.FrozenRun,
	*batchv1.Job,
	evidencestore.ObjectIdentity,
) (profileradmission.Result, error) {
	a.calls++
	return profileradmission.Result{}, errors.New("unexpected admission")
}

type reconcilerHarness struct {
	reconciler *profilercontroller.RunReconciler
	freezer    *staticIntentFreezer
	jobs       *apiExecutionJobs
	finalizer  *rejectingFinalizer
	admitter   *rejectingAdmitter
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
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	run := validRun()
	if err := kube.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
		t.Fatal(err)
	}
	run.Status.IntentDigest = digest
	run.Status.ObservedGeneration = run.Generation
	if err := kube.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	return run, profiler.PreparedRun{Intent: intent, IntentDigest: digest}
}

func newReconcilerHarness(
	t *testing.T,
	kube client.Client,
	prepared profiler.PreparedRun,
) *reconcilerHarness {
	t.Helper()
	freezer := &staticIntentFreezer{prepared: prepared}
	jobs := &apiExecutionJobs{kube: kube}
	finalizer := &rejectingFinalizer{}
	admitter := &rejectingAdmitter{}
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
	finalizer *rejectingFinalizer,
	admitter *rejectingAdmitter,
) *profilercontroller.RunReconciler {
	t.Helper()
	reconciler, err := profilercontroller.NewRunReconciler(kube, freezer, jobs, finalizer, admitter)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
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
				Batch: 1, WarmupIterations: 20, DurationSeconds: 30, Repetitions: 1,
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
