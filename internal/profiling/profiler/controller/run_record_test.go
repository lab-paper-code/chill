package controller

import (
	"context"
	"reflect"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRunRecordsRoundTripJobCarriedRunConfig(t *testing.T) {
	run := testRun()
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

	reader := fake.NewClientBuilder().
		WithScheme(executionJobTestScheme(t)).
		WithObjects(job).
		Build()
	records, err := NewRunRecords(reader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := records.Load(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Prepared.RunConfig, want.Prepared.RunConfig) ||
		!reflect.DeepEqual(got.AdmissionPolicy, want.AdmissionPolicy) ||
		!reflect.DeepEqual(got.ExecutionPolicy, want.ExecutionPolicy) {
		t.Fatalf("Job RunConfig round trip drifted:\n got: %#v\nwant: %#v", got, want)
	}
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.ConfigMap != nil {
			t.Fatalf("Job retained a separate ConfigMap record volume: %#v", volume)
		}
	}
}
