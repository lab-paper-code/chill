package controller

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestProjectRequestMapsOnlyCallerSelectedPolicy(t *testing.T) {
	run := testRun()
	request, err := ProjectRequest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if request.Preparation.Candidate.ModelSpec != run.Spec.Candidate.ModelSpec ||
		request.Preparation.Candidate.PowerMode != run.Spec.Candidate.PowerMode ||
		request.Preparation.TargetNode != run.Spec.TargetNode || request.Preparation.CPURequest != "100m" ||
		request.Preparation.Measurement.Batch != 1 || request.Preparation.Metrics.IntervalMilliseconds != 1000 {
		t.Fatalf("ProfilingRun request projection drifted: %#v", request)
	}
}

func TestProjectRequestDurationToleranceDefaultAndSelection(t *testing.T) {
	run := testRun()
	request, err := ProjectRequest(run.Spec)
	if err != nil || request.Preparation.Measurement.DurationToleranceSeconds != .5 {
		t.Fatalf("omitted tolerance must retain 0.5 seconds: %+v, %v", request, err)
	}
	tolerance := .25
	run.Spec.Measurement.DurationToleranceSeconds = &tolerance
	request, err = ProjectRequest(run.Spec)
	if err != nil || request.Preparation.Measurement.DurationToleranceSeconds != tolerance {
		t.Fatalf("selected tolerance was not frozen: %+v, %v", request, err)
	}
}

func TestProjectRequestPreservesIdleSettlingContract(t *testing.T) {
	run := testRun()
	settling := int32(60)
	run.Spec.Measurement.WarmupIterations = 0
	run.Spec.Measurement.SettlingDurationSeconds = &settling
	run.Spec.Measurement.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	run.Spec.Measurement.Dataset = ""

	request, err := ProjectRequest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	measurement := request.Preparation.Measurement
	if measurement.SettlingDurationSeconds != 60 ||
		measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		t.Fatalf("idle request projection drifted: %#v", request)
	}
}

func TestProjectRequestMovesCPUQuotaOnlyToPreparationSelection(t *testing.T) {
	run := testRun()
	quota := resource.MustParse("4")
	run.Spec.Measurement.CPUQuota = &quota

	request, err := ProjectRequest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if request.Preparation.CPUQuota != "4" {
		t.Fatalf("projected CPU quota = %q", request.Preparation.CPUQuota)
	}
}

func TestProjectRequestAffinityMatchesFrozenReconstruction(t *testing.T) {
	run := testRun()
	run.Spec.Measurement.CPUAffinity = []int32{31, 12, 10, 8}
	request, err := ProjectRequest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(request.Preparation.CPUAffinity, []int{8, 10, 12, 31}) ||
		!slices.Equal(run.Spec.Measurement.CPUAffinity, []int32{31, 12, 10, 8}) {
		t.Fatal("projection did not canonicalize a detached copy")
	}
	config := testRunConfig(t)
	config.State.RuntimeFamily = profilingrun.FirstfloorRuntimeFamily
	config.State.Backend = profilingrun.FirstfloorORTCPUBackend
	config.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	config.CPU.AffinityCPUs = slices.Clone(request.Preparation.CPUAffinity)
	prepared := profiler.PreparedRun{RunConfig: config}
	if err := validatePreparedForRun(run, prepared); err != nil {
		t.Fatalf("unsorted API mask did not match canonical frozen mask: %v", err)
	}
	reconstructed := preparationFromRunConfig(config)
	reconstructed.CPUAffinity[0] = 9
	if config.CPU.AffinityCPUs[0] != 8 {
		t.Fatal("request reconstruction aliases frozen affinity")
	}
	prepared.RunConfig.CPU.AffinityCPUs = []int{9, 10, 12, 31}
	if err := validatePreparedForRun(run, prepared); err == nil {
		t.Fatal("different exact mask passed RunConfig reconstruction")
	}
	for _, mask := range [][]int32{{-1, 8}, {8, 8}} {
		run.Spec.Measurement.CPUAffinity = mask
		if _, err := ProjectRequest(run.Spec); err == nil {
			t.Fatalf("invalid API mask accepted: %v", mask)
		}
	}
}

func TestProjectRequestRejectsSemanticInvalidity(t *testing.T) {
	run := testRun()
	run.Spec.CPURequest = resource.MustParse("-1")
	if _, err := ProjectRequest(run.Spec); err == nil {
		t.Fatal("expected negative CPU request rejection")
	}
}

func TestProjectSnapshotDrivesLifecycleFromObservedFacts(t *testing.T) {
	config := testRunConfig(t)
	tests := []struct {
		name      string
		configure func(*edgev1alpha1.ProfilingRun) *batchv1.Job
		want      profiler.Action
	}{
		{
			name: "create Job from resolved RunConfig",
			configure: func(run *edgev1alpha1.ProfilingRun) *batchv1.Job {
				return nil
			},
			want: profiler.ActionCreateJob,
		},
		{
			name: "wait for active Job",
			configure: func(run *edgev1alpha1.ProfilingRun) *batchv1.Job {
				recordRunConfigAndJob(run)
				return ownedJob(run, nil)
			},
			want: profiler.ActionWaitForJob,
		},
		{
			name: "finalize successful Job",
			configure: func(run *edgev1alpha1.ProfilingRun) *batchv1.Job {
				recordRunConfigAndJob(run)
				return ownedJob(run, &batchv1.JobStatus{
					Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
				})
			},
			want: profiler.ActionFinalizeEvidence,
		},
		{
			name: "complete after terminal evidence invalidity",
			configure: func(run *edgev1alpha1.ProfilingRun) *batchv1.Job {
				recordRunConfigAndJob(run)
				run.Status.Conditions = []metav1.Condition{{
					Type:   edgev1alpha1.ProfilingRunConditionEvidenceVerified,
					Status: metav1.ConditionFalse, Reason: evidenceReasonInvalid,
				}}
				return ownedJob(run, &batchv1.JobStatus{
					Succeeded: 1,
					Conditions: []batchv1.JobCondition{{
						Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
					}},
				})
			},
			want: profiler.ActionComplete,
		},
		{
			name: "do not recreate missing recorded Job",
			configure: func(run *edgev1alpha1.ProfilingRun) *batchv1.Job {
				recordRunConfigAndJob(run)
				return nil
			},
			want: profiler.ActionReportExecutionLost,
		},
		{
			name: "admit retained evidence after Job disappears",
			configure: func(run *edgev1alpha1.ProfilingRun) *batchv1.Job {
				recordRunConfigAndJob(run)
				run.Status.Evidence = testEvidenceReference()
				return nil
			},
			want: profiler.ActionComplete,
		},
		{
			name: "complete with stored evidence",
			configure: func(run *edgev1alpha1.ProfilingRun) *batchv1.Job {
				recordRunConfigAndJob(run)
				run.Status.Evidence = testEvidenceReference()

				return nil
			},
			want: profiler.ActionComplete,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := testRun()
			job := test.configure(run)
			snapshot, err := ProjectSnapshot(run, &config, job)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := profiler.Decide(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Action != test.want {
				t.Fatalf("expected %s, got %s", test.want, decision.Action)
			}
		})
	}
}

func TestProjectSnapshotRejectsIdentityAndOutcomeContradictions(t *testing.T) {
	config := testRunConfig(t)
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.ProfilingRun, *batchv1.Job)
		match  string
	}{
		{
			name: "foreign Job owner",
			mutate: func(_ *edgev1alpha1.ProfilingRun, job *batchv1.Job) {
				job.OwnerReferences[0].UID = "other-run"
			},
			match: "not controlled",
		},
		{
			name: "conflicting terminal conditions",
			mutate: func(_ *edgev1alpha1.ProfilingRun, job *batchv1.Job) {
				job.Status.Conditions = []batchv1.JobCondition{
					{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
					{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
				}
			},
			match: "both Complete and Failed",
		},
		{
			name: "unsupported evidence schema",
			mutate: func(run *edgev1alpha1.ProfilingRun, _ *batchv1.Job) {
				run.Status.Evidence = testEvidenceReference()
				run.Status.Evidence.SchemaVersion = "other"
			},
			match: "unsupported execution evidence",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := testRun()
			recordRunConfigAndJob(run)
			job := ownedJob(run, nil)
			test.mutate(run, job)
			_, err := ProjectSnapshot(run, &config, job)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestProjectSnapshotStopsCleanlyDuringDeletion(t *testing.T) {
	run := testRun()
	now := metav1.NewTime(time.Now())
	run.DeletionTimestamp = &now
	snapshot, err := ProjectSnapshot(run, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := profiler.Decide(snapshot)
	if err != nil || decision.Action != profiler.ActionStop {
		t.Fatalf("expected deletion stop, got %#v, %v", decision, err)
	}
}

func testRun() *edgev1alpha1.ProfilingRun {
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run", UID: "run-uid"},
		Spec: edgev1alpha1.ProfilingRunSpec{
			MeasurementStateSpec: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec:     "mobilenet-v2-050",
					DeviceClass:   "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu",
					PowerMode:     "fixed",
					Preprocessor:  "mobilenet-imagenet",
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

func testRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	payload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	// Historical raw fixture is preserved; this helper builds a current request.
	config.Measurement.DurationToleranceSeconds = .5
	return config
}

func recordRunConfigAndJob(run *edgev1alpha1.ProfilingRun) {
	run.Status.Job = &edgev1alpha1.ProfilingRunObjectReference{Name: "run-job", UID: "job-uid"}
}

func ownedJob(run *edgev1alpha1.ProfilingRun, status *batchv1.JobStatus) *batchv1.Job {
	truth := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: run.Namespace, Name: "run-job", UID: types.UID("job-uid"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "ProfilingRun",
				Name: run.Name, UID: run.UID, Controller: &truth,
			}},
		},
	}
	if status != nil {
		job.Status = *status
	}
	return job
}

func testEvidenceReference() *edgev1alpha1.ProfilingRunEvidenceReference {
	return &edgev1alpha1.ProfilingRunEvidenceReference{
		Store: "chill-evidence", Key: "runs/run-uid/execution.json",
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion, Size: 1024,
	}
}
