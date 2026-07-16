package resources

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var _ = Describe("ProfilingRun API", func() {
	It("accepts one minimum immutable CPU ORT Run request", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("valid"))

		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })
	})

	DescribeTable("rejects structurally invalid Run requests",
		func(mutate func(*edgev1alpha1.ProfilingRun)) {
			ctx := context.Background()
			run := validProfilingRun(uniqueProfilingRunName("invalid"))
			mutate(run)

			err := k8sClient.Create(ctx, run)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("empty ModelSpec", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Candidate.ModelSpec = ""
		}),
		Entry("empty power mode", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Candidate.PowerMode = ""
		}),
		Entry("zero batch", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.Batch = 0
		}),
		Entry("multiple repetitions", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.Repetitions = 2
		}),
		Entry("non-positive CPU request", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.CPURequest = resource.MustParse("-1")
		}),
		Entry("power timeout not below interval", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.PowerObservation.TimeoutMilliseconds = run.Spec.PowerObservation.IntervalMilliseconds
		}),
		Entry("power window shorter than measurement", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.PowerObservation.DurationSeconds = run.Spec.Measurement.DurationSeconds - 1
		}),
		Entry("empty admission policy", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.AdmissionPolicy = ""
		}),
	)

	It("rejects changing an existing Run spec", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("immutable"))
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })

		run.Spec.Measurement.Batch = 2
		err := k8sClient.Update(ctx, run)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("accepts compact status without raw measurement data", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("status"))
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })

		run.Status = edgev1alpha1.ProfilingRunStatus{
			ObservedGeneration: run.Generation,
			IntentDigest:       "sha256:" + strings.Repeat("a", 64),
			Conditions: []metav1.Condition{{
				Type: edgev1alpha1.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionTrue,
				Reason: "JobSucceeded", LastTransitionTime: metav1.Now(),
			}},
			Job: &edgev1alpha1.ProfilingRunObjectReference{Name: "run-job", UID: "job-uid"},
			ActualExecution: &edgev1alpha1.ProfilingRunActualExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "run-pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "lattepanda", UID: "node-uid"},
			},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: "chill-evidence", Key: "runs/run-uid/execution.json",
				Digest:        "sha256:" + strings.Repeat("b", 64),
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha1", Size: 1024,
			},
			Admission: &edgev1alpha1.ProfilingRunAdmissionStatus{
				Verdict: "Insufficient", PolicyVersion: "wall-power-v1",
				ReasonCodes: []string{"ThermalStateUnavailable"},
			},
		}
		Expect(k8sClient.Status().Update(ctx, run)).To(Succeed())
	})
})

func uniqueProfilingRunName(prefix string) string {
	return fmt.Sprintf("profilingrun-%s-%d", prefix, time.Now().UnixNano())
}

func validProfilingRun(name string) *edgev1alpha1.ProfilingRun {
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: edgev1alpha1.ProfilingRunSpec{
			Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
				ModelSpec:     "mobilenet-v2-050",
				DeviceClass:   "lattepanda-3-delta-8g",
				ExecutionPath: "ort-cpu",
				PowerMode:     "fixed",
			},
			TargetNode: "lattepanda",
			CPURequest: resource.MustParse("100m"),
			Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{
				Batch: 1, WarmupIterations: 20, DurationSeconds: 30, Repetitions: 1,
				WorkloadMode:     "saturated-serial-fixed-batch",
				InputGenerator:   "numpy-default-rng-seed-0-fp32-v1",
				OutputValidation: "nonempty-shape-batch-v1",
			},
			PowerObservation: edgev1alpha1.ProfilingRunPowerObservationSpec{
				IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
			},
			AdmissionPolicy: "wall-power-v1",
		},
	}
}
