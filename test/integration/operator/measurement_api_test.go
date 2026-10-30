package operatorintegration

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var _ = Describe("OperationCostMeasurement API", func() {
	It("accepts an ordered power, prepare, idle and remove sequence", func() {
		ctx := context.Background()
		request := validOperationCostMeasurement(uniqueMeasurementName("opcost"))
		Expect(k8sClient.Create(ctx, request)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, request) })
	})

	DescribeTable("rejects steps with fields of another kind",
		func(mutate func(*edgev1alpha1.OperationCostMeasurement)) {
			request := validOperationCostMeasurement(uniqueMeasurementName("opcost-invalid"))
			mutate(request)
			err := k8sClient.Create(context.Background(), request)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("power without a mode", func(r *edgev1alpha1.OperationCostMeasurement) { r.Spec.Steps[0].PowerMode = "" }),
		Entry("power with an option", func(r *edgev1alpha1.OperationCostMeasurement) {
			r.Spec.Steps[0].BatchEnergyOptimization = "beo"
		}),
		Entry("prepare without an option", func(r *edgev1alpha1.OperationCostMeasurement) {
			r.Spec.Steps[1].BatchEnergyOptimization = ""
		}),
		Entry("idle without a duration", func(r *edgev1alpha1.OperationCostMeasurement) { r.Spec.Steps[2].IdleDurationSeconds = nil }),
		Entry("remove with a duration", func(r *edgev1alpha1.OperationCostMeasurement) {
			r.Spec.Steps[3].IdleDurationSeconds = ptr.To[int32](1)
		}),
		Entry("duplicate step names", func(r *edgev1alpha1.OperationCostMeasurement) { r.Spec.Steps[3].Name = "prepare" }),
		Entry("request timeout not below interval", func(r *edgev1alpha1.OperationCostMeasurement) {
			r.Spec.Observation.RequestTimeoutMilliseconds = 1000
		}),
		Entry("missing empirical timing", func(r *edgev1alpha1.OperationCostMeasurement) { r.Spec.Observation.Timing = nil }),
	)

	It("rejects changing an existing request", func() {
		ctx := context.Background()
		request := validOperationCostMeasurement(uniqueMeasurementName("opcost-immutable"))
		Expect(k8sClient.Create(ctx, request)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, request) })
		request.Spec.Node = "other"
		err := k8sClient.Update(ctx, request)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})
})

var _ = Describe("DispatchOccupancyMeasurement API", func() {
	It("accepts one saturation measurement", func() {
		ctx := context.Background()
		request := validDispatchOccupancyMeasurement(uniqueMeasurementName("occupancy"))
		Expect(k8sClient.Create(ctx, request)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, request) })
	})

	It("accepts a positive configured open-loop window and rate above one", func() {
		request := validDispatchOccupancyMeasurement(uniqueMeasurementName("occupancy-configured"))
		request.Spec.DurationSeconds, request.Spec.RateFactor = 30, "1.01"
		Expect(k8sClient.Create(context.Background(), request)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(context.Background(), request) })
	})

	DescribeTable("rejects a load that cannot saturate or a window that cannot judge",
		func(mutate func(*edgev1alpha1.DispatchOccupancyMeasurement)) {
			request := validDispatchOccupancyMeasurement(uniqueMeasurementName("occupancy-invalid"))
			mutate(request)
			err := k8sClient.Create(context.Background(), request)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("rate factor not above one", func(r *edgev1alpha1.DispatchOccupancyMeasurement) { r.Spec.RateFactor = "1" }),
		Entry("zero window", func(r *edgev1alpha1.DispatchOccupancyMeasurement) { r.Spec.DurationSeconds = 0 }),
		Entry("idle fraction above one", func(r *edgev1alpha1.DispatchOccupancyMeasurement) { r.Spec.MaximumIdleFraction = "1.5" }),
		Entry("zero batch", func(r *edgev1alpha1.DispatchOccupancyMeasurement) { r.Spec.Batch = 0 }),
	)

	It("rejects changing an existing request", func() {
		ctx := context.Background()
		request := validDispatchOccupancyMeasurement(uniqueMeasurementName("occupancy-immutable"))
		Expect(k8sClient.Create(ctx, request)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, request) })
		request.Spec.RateFactor = "2"
		err := k8sClient.Update(ctx, request)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})
})

func uniqueMeasurementName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func validOperationCostMeasurement(name string) *edgev1alpha1.OperationCostMeasurement {
	return &edgev1alpha1.OperationCostMeasurement{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: edgev1alpha1.OperationCostMeasurementSpec{
			Node: "jetsono",
			Observation: edgev1alpha1.MeasurementObservationSpec{
				IntervalMilliseconds: 1000, RequestTimeoutMilliseconds: 800, MaximumOperationSeconds: 2250,
				MaximumGapMilliseconds: 3000,
				// Synthetic request uses the existing Accepted055 empirical timing pattern.
				Timing: &edgev1alpha1.MeasurementTimingSpec{
					ChangeIntervalP95Seconds: "1.0339932335", MaximumRequestSeconds: "0.24872398",
					MaximumReceiptGapSeconds: "0.325263383", Source: "synthetic API fixture using completed 120s observation inputs",
				},
			},
			Steps: []edgev1alpha1.OperationCostStep{
				{Name: "up", Kind: edgev1alpha1.OperationCostStepPower, PowerMode: "MAXN"},
				{Name: "prepare", Kind: edgev1alpha1.OperationCostStepPrepare, BatchEnergyOptimization: "beo"},
				{Name: "idle", Kind: edgev1alpha1.OperationCostStepIdle, IdleDurationSeconds: ptr.To[int32](120)},
				{Name: "remove", Kind: edgev1alpha1.OperationCostStepRemove, BatchEnergyOptimization: "beo"},
			},
		},
	}
}

func validDispatchOccupancyMeasurement(name string) *edgev1alpha1.DispatchOccupancyMeasurement {
	return &edgev1alpha1.DispatchOccupancyMeasurement{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: edgev1alpha1.DispatchOccupancyMeasurementSpec{
			Node: "rasp5", BatchEnergyOptimization: "beo", Workload: "plc", Batch: 1, RateFactor: "1.2",
			WarmupSeconds: 60, DurationSeconds: 360, MinimumBatches: 120, MaximumIdleFraction: "0.01", Workers: 128,
		},
	}
}
