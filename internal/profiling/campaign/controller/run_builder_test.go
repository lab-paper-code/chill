package controller

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRunNameIsDeterministic(t *testing.T) {
	name := RunName("jetsono-ortcpu-m50", batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 2,
	}, 1)
	if name != "jetsono-ortcpu-m50-active-b2-a1" {
		t.Fatalf("name = %q", name)
	}
}

func TestBuildRunFillsTheIdleUnionFields(t *testing.T) {
	parent := testParent()
	run, err := BuildRun(parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1,
	}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	measurement := run.Spec.Measurement
	if measurement.WorkloadMode != "idle-model-loaded" || measurement.WarmupIterations != 0 ||
		measurement.InputGenerator != "none" || measurement.OutputValidation != "none" ||
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
	}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	measurement := run.Spec.Measurement
	if measurement.WorkloadMode != "saturated-serial-fixed-batch" || measurement.Batch != 2 ||
		measurement.SettlingDurationSeconds != nil ||
		measurement.WarmupIterations != parent.Spec.ActiveProtocol.WarmupIterations {
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
					ExecutionPath: "ort-cpu", PowerMode: "MODE_50W",
				},
				TargetNode: "jetsono", CPURequest: resource.MustParse("100m"),
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{
				WarmupIterations: 20, DurationSeconds: 75,
				InputGenerator:   "numpy-default-rng-seed-0-inputs-v1",
				OutputValidation: "nonempty-shape-batch-v1",
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
			RetryLimit:     2,
		},
	}
}
