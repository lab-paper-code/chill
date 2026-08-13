package operatorintegration

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

const (
	idleAdmissionPolicy    = "idle-wall-power-v1"
	invalidAdmissionPolicy = "Invalid Policy"
	noProtocol             = "none"
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

	It("accepts an operator-configured admission policy name", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("configured-policy"))
		run.Spec.AdmissionPolicy = "lab-wall-meter-2026.08"

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
		Entry("non-positive CPU request", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.CPURequest = resource.MustParse("-1")
		}),
		Entry("power timeout not below interval", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.MetricObservation.TimeoutMilliseconds = run.Spec.MetricObservation.IntervalMilliseconds
		}),
		Entry("power window shorter than measurement", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.MetricObservation.DurationSeconds = run.Spec.Measurement.DurationSeconds - 1
		}),
		Entry("empty admission policy", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.AdmissionPolicy = ""
		}),
		Entry("malformed admission policy name", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.AdmissionPolicy = invalidAdmissionPolicy
		}),
		Entry("idle mode without settling duration", func(run *edgev1alpha1.ProfilingRun) {
			run.Spec.Measurement.WarmupIterations = 0
			run.Spec.Measurement.WorkloadMode = "idle-model-loaded"
			run.Spec.Measurement.InputGenerator = noProtocol
			run.Spec.Measurement.OutputValidation = noProtocol
			run.Spec.AdmissionPolicy = idleAdmissionPolicy
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
			IntentFrozen:       true,
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
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha8", Size: 1024,
			},
			Admission: &edgev1alpha1.ProfilingRunAdmissionStatus{
				Verdict: "Insufficient", PolicyVersion: "wall-power-v2",
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
			MeasurementStateSpec: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec:     "mobilenet-v2-050",
					DeviceClass:   "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu",
					PowerMode:     "fixed",
				},
				TargetNode: "lattepanda",
				CPURequest: resource.MustParse("100m"),
			},
			AdmissionPolicy: "wall-power-v2",
			Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{
				Batch: 1, WarmupIterations: 20, DurationSeconds: 30,
				WorkloadMode:     "saturated-serial-fixed-batch",
				InputGenerator:   "numpy-default-rng-seed-0-fp32-v1",
				OutputValidation: "nonempty-shape-batch-v1",
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
	run.Spec.Measurement.InputGenerator = noProtocol
	run.Spec.Measurement.OutputValidation = noProtocol
	run.Spec.AdmissionPolicy = idleAdmissionPolicy
}
