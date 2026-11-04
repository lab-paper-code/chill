package operatorintegration

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	It("accepts one model-loaded idle Run request", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("idle"))
		configureIdleProfilingRun(run)

		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })
	})

	It("accepts one batch-fixed model-loaded idle Run request", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("idle-batch-fixed"))
		configureIdleProfilingRun(run)
		run.Spec.Measurement.Batch = 2

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
		Entry("empty preprocessor", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Candidate.Preprocessor = ""
		}),
		Entry("active mode without dataset", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.Dataset = ""
		}),
		Entry("active mode without Workload", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.Workload = ""
		}),
		Entry("idle mode with dataset", func(run *edgev1alpha1.ProfilingRun) {
			configureIdleProfilingRun(run)
			run.Spec.Measurement.Dataset = "vqav2-val-subset"
		}),
		Entry("empty power mode", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Candidate.PowerMode = ""
		}),
		Entry("zero batch", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.Batch = 0
		}),
		Entry("power timeout not below interval", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.MetricObservation.TimeoutMilliseconds = run.Spec.MetricObservation.IntervalMilliseconds
		}),
		Entry("power window shorter than measurement", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.MetricObservation.DurationSeconds = run.Spec.Measurement.DurationSeconds - 1
		}),
		Entry("idle mode without settling duration", func(run *edgev1alpha1.ProfilingRun) {
			configureIdleProfilingRun(run)
			run.Spec.Measurement.SettlingDurationSeconds = nil
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
			Conditions: []metav1.Condition{{
				Type: edgev1alpha1.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionTrue,
				Reason: "JobSucceeded", LastTransitionTime: metav1.Now(),
			}},
			Job: &edgev1alpha1.ProfilingRunObjectReference{Name: "run-job", UID: "job-uid"},
			ObservedExecution: &edgev1alpha1.ProfilingRunObservedExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "run-pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "lattepanda", UID: "node-uid"},
			},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: "chill-evidence", Key: "runs/run-uid/execution.json",
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha14", Size: 1024,
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
			MeasurementStateSpec: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec:     "mobilenet-v2-050",
					DeviceClass:   "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu",
					Preprocessor:  "mobilenet-imagenet",
					PowerMode:     "fixed",
				},
				TargetNode: "lattepanda",
			},
			Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{
				Batch: 1, WarmupIterations: 20, DurationSeconds: 30,
				WorkloadMode: "saturated-serial-fixed-batch",
				Dataset:      "vqav2-val-subset",
				Workload:     "mobilenet", WorkloadUID: "workload-uid", WorkloadResourceVersion: "1",
				SLOPercentile: 95, SLOTargetLatencyNanos: int64(time.Second), MinServiceSamples: 59,
			},
			MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
				IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
			},
		},
	}
}

func configureIdleProfilingRun(run *edgev1alpha1.ProfilingRun) {
	settling := int32(60)
	run.Spec.Measurement.WarmupIterations = 0
	run.Spec.Measurement.SettlingDurationSeconds = &settling
	run.Spec.Measurement.WorkloadMode = "idle-model-loaded"
	run.Spec.Measurement.Dataset = ""
	run.Spec.Measurement.Workload = ""
	run.Spec.Measurement.WorkloadUID = ""
	run.Spec.Measurement.WorkloadResourceVersion = ""
	run.Spec.Measurement.SLOPercentile = 0
	run.Spec.Measurement.SLOTargetLatencyNanos = 0
	run.Spec.Measurement.MinServiceSamples = 0
}
