package controller

import (
	"reflect"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestRunRecordsRoundTripJobCarriedRunConfig(t *testing.T) {
	run := testRun()
	run.Spec.Measurement.StartupWarmupIterations = 0
	run.Spec.Measurement.WarmupBatchSizes = nil
	want := resolvedRunForPrepared(t, currentPreparedRun(t))
	job, err := buildExecutionJob(
		run,
		want,
		testEvidenceStoreBinding(),
		testExecutionJobOptions(),
		executionJobTestScheme(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	job.UID = testJobUID
	run.Status.Job = &edgev1alpha1.ProfilingRunObjectReference{Name: job.Name, UID: string(job.UID)}

	got, err := resolvedRunFromJob(run, job)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Prepared.RunConfig, want.Prepared.RunConfig) ||
		!reflect.DeepEqual(got.ExecutionPolicy, want.ExecutionPolicy) {
		t.Fatalf("Job RunConfig round trip drifted:\n got: %#v\nwant: %#v", got, want)
	}
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.ConfigMap != nil {
			t.Fatalf("Job retained a separate ConfigMap record volume: %#v", volume)
		}
	}
}

func TestValidatePreparedForRunSeparatesResolvedWorkloadFacts(t *testing.T) {
	prepared := currentPreparedRun(t)
	measurement := &prepared.RunConfig.Measurement
	measurement.SLOPercentile = 99
	minimum, err := profilingrun.MinimumServiceSamples(measurement.SLOPercentile)
	if err != nil {
		t.Fatal(err)
	}
	measurement.MinServiceSamples = minimum
	measurement.SLOTargetLatencyNanos = 2_000_000_000
	measurement.WorkloadUID = "resolved-workload-uid"
	measurement.WorkloadResourceVersion = "42"
	if err := validateResolvedRunForRun(testRun(), resolvedRunForPrepared(t, prepared)); err != nil {
		t.Fatalf("resolved Workload facts are not caller-selected request fields: %v", err)
	}
}

func TestValidatePreparedForRunRejectsSelectedMeasurementDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*edgev1alpha1.ProfilingRun)
	}{
		{name: "Workload", mutate: func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.Workload = "different-workload"
		}},
		{name: "startup warmup", mutate: func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.StartupWarmupIterations++
		}},
		{name: "warmup domain", mutate: func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.WarmupBatchSizes = []int32{1, 2}
		}},
		{name: "batch", mutate: func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.Batch++
		}},
		{name: "dataset", mutate: func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.Dataset = "different-dataset"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := testRun()
			test.mutate(run)
			if err := validatePreparedForRun(run, currentPreparedRun(t)); err == nil ||
				err.Error() != "RunConfig does not match ProfilingRun spec" {
				t.Fatalf("selected measurement drift must be rejected, got %v", err)
			}
		})
	}
}
