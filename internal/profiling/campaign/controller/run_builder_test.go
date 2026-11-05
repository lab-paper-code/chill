package controller

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRunNameIsDeterministic(t *testing.T) {
	name := batchenergyoptimization.RunName("jetsono-ortcpu-m50", batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}, 1)
	if name != "jetsono-ortcpu-m50-active-b2-a1" {
		t.Fatalf("name = %q", name)
	}
}

func TestBuildIdleRunSeparatesMeasurementBatchFromTrialIdentity(t *testing.T) {
	for _, test := range []struct{ path, backend string }{
		{"ort-cpu", profilingrun.FirstfloorORTCPUBackend},
		{"ort-cuda", profilingrun.FirstfloorORTCUDABackend},
		{profilingrun.RKNNLiteExecutionPath, profilingrun.FirstfloorRKNNLiteBackend},
		{"rknn-bs4", profilingrun.FirstfloorRKNNLiteBackend},
	} {
		t.Run(test.path, func(t *testing.T) {
			parent := testParent()
			parent.Spec.State.Candidate.ExecutionPath = test.path
			measurement := batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 4}
			run, err := BuildRun(parent, measurement, 2, test.backend)
			if err != nil {
				t.Fatal(err)
			}
			want := int32(1)
			if test.backend == profilingrun.FirstfloorRKNNLiteBackend {
				want = 4
			}
			if run.Spec.Measurement.Batch != want {
				t.Fatalf("measurement batch = %d, want %d", run.Spec.Measurement.Batch, want)
			}
			if run.Spec.CampaignTrial == nil || run.Spec.CampaignTrial.Batch != 4 || run.Spec.CampaignTrial.Attempt != 2 {
				t.Fatalf("logical identity = %+v", run.Spec.CampaignTrial)
			}
			repeated, err := BuildRun(parent, measurement, 2, test.backend)
			if err != nil || repeated.Name != run.Name {
				t.Fatalf("deterministic requery = %+v, error = %v", repeated, err)
			}
			run.Labels[labelBatch], run.Labels[labelAttempt] = "99", "99"
			trial, err := projectOwnedTrial(parent, run)
			if err != nil || trial.Measurement != measurement || trial.Attempt != 2 {
				t.Fatalf("projected trial = %+v, error = %v", trial, err)
			}
			run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{Key: "verified"}
			completed, _, err := projectResultTrials(parent, []edgev1alpha1.ProfilingRun{*run})
			if err != nil || len(completed) != 1 || completed[0].Measurement.Batch != 4 {
				t.Fatalf("result lost logical identity: %+v, error = %v", completed, err)
			}
		})
	}
}

func TestCampaignProjectionRejectsMissingOrContradictoryTrialIdentity(t *testing.T) {
	for _, mutate := range []func(*edgev1alpha1.ProfilingRun){
		func(run *edgev1alpha1.ProfilingRun) { run.Spec.CampaignTrial = nil },
		func(run *edgev1alpha1.ProfilingRun) { run.Spec.CampaignTrial.Batch = 0 },
		func(run *edgev1alpha1.ProfilingRun) { run.Spec.CampaignTrial.Attempt = 0 },
		func(run *edgev1alpha1.ProfilingRun) { run.Spec.Measurement.WorkloadMode = activeWorkloadMode },
		func(run *edgev1alpha1.ProfilingRun) { run.Spec.CampaignTrial.Batch = 8 },
	} {
		parent := testParent()
		run, err := BuildRun(parent, batchenergyoptimization.Measurement{
			WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 4,
		}, 2, "ort-cpu")
		if err != nil {
			t.Fatal(err)
		}
		mutate(run)
		if _, err := projectOwnedTrial(parent, run); err == nil {
			t.Fatalf("accepted missing/contradictory identity: %+v", run.Spec)
		}
	}
}

func TestBuildRunFillsTheIdleUnionFields(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1,
	}, 1, "ort-cpu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	measurement := run.Spec.Measurement
	if measurement.WorkloadMode != "idle-model-loaded" || measurement.WarmupIterations != 0 ||
		measurement.Dataset != "" ||
		measurement.SettlingDurationSeconds == nil {
		t.Fatalf("idle Run does not satisfy the ProfilingRun union rule: %+v", measurement)
	}
	if run.Spec.MeasurementStateSpec != parent.Spec.State {
		t.Fatalf("Run did not inherit the shared state")
	}
	owner := run.OwnerReferences
	if len(owner) != 1 || owner[0].Controller == nil || !*owner[0].Controller ||
		string(owner[0].UID) != string(parent.UID) {
		t.Fatalf("Run is not controller-owned by its parent: %+v", owner)
	}
}

func TestBuildRunUsesTheActiveProtocol(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}, 1, "ort-cpu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	measurement := run.Spec.Measurement
	if measurement.WorkloadMode != "empirical-cadence-fixed-batch" || measurement.Batch != 2 ||
		measurement.SettlingDurationSeconds != nil ||
		measurement.WarmupIterations != 0 ||
		measurement.StartupWarmupIterations != parent.Spec.ActiveProtocol.WarmupIterations {
		t.Fatalf("active Run does not match the active protocol: %+v", measurement)
	}
}

func testParent() *edgev1alpha1.BatchEnergyOptimization {
	return &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{
			Name: "jetsono-ortcpu-m50", Namespace: "chill-system", UID: "parent-uid",
		},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec: "mobilenet-v2-050", DeviceClass: "jetson-agx-orin-64g",
					ExecutionPath: "ort-cpu", PowerMode: "MODE_50W", Preprocessor: "mobilenet-imagenet",
				},
				TargetNode: "jetsono",
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{
				WarmupIterations: 20, DurationSeconds: 75,
				Workload: "mobilenet",
				Dataset:  "vqav2-val-subset",
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 2500, TimeoutMilliseconds: 2400, DurationSeconds: 78,
				},
			},
			IdleProtocol: edgev1alpha1.IdleProtocolSpec{
				SettlingDurationSeconds: 60, DurationSeconds: 30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000, TimeoutMilliseconds: 900, DurationSeconds: 31,
				},
			},
			TrialsPerBatch: 3,
			RetryLimit: 2,
		},
	}
}
