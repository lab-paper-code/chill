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

var _ = Describe("ModelLoadedIdleBaseline API", func() {
	It("accepts one minimum immutable idle-only aggregate request", func() {
		ctx := context.Background()
		request := validModelLoadedIdleBaseline(uniqueIdleBaselineName("valid"))
		Expect(k8sClient.Create(ctx, request)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, request) })
	})

	DescribeTable("rejects invalid aggregate bounds",
		func(mutate func(*edgev1alpha1.ModelLoadedIdleBaseline)) {
			ctx := context.Background()
			request := validModelLoadedIdleBaseline(uniqueIdleBaselineName("invalid"))
			mutate(request)
			err := k8sClient.Create(ctx, request)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("fewer than three accepted Runs", func(request *edgev1alpha1.ModelLoadedIdleBaseline) {
			request.Spec.PointPolicy.MinimumAcceptedRuns = 2
		}),
		Entry("attempt limit below accepted target", func(request *edgev1alpha1.ModelLoadedIdleBaseline) {
			request.Spec.AttemptLimit = 3
			request.Spec.PointPolicy.MinimumAcceptedRuns = 4
		}),
		Entry("active-style zero batch", func(request *edgev1alpha1.ModelLoadedIdleBaseline) {
			request.Spec.Batch = 0
		}),
		Entry("confidence outside the open interval", func(request *edgev1alpha1.ModelLoadedIdleBaseline) {
			request.Spec.PointPolicy.ConfidenceLevel = "1.0"
		}),
	)

	It("rejects changing an existing aggregate request", func() {
		ctx := context.Background()
		request := validModelLoadedIdleBaseline(uniqueIdleBaselineName("immutable"))
		Expect(k8sClient.Create(ctx, request)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, request) })
		request.Spec.AttemptLimit++
		err := k8sClient.Update(ctx, request)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})
})

func uniqueIdleBaselineName(prefix string) string {
	return fmt.Sprintf("idle-baseline-%s-%d", prefix, time.Now().UnixNano())
}

func validModelLoadedIdleBaseline(name string) *edgev1alpha1.ModelLoadedIdleBaseline {
	return &edgev1alpha1.ModelLoadedIdleBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: edgev1alpha1.ModelLoadedIdleBaselineSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec: "mobilenet-v2-050", DeviceClass: "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu", PowerMode: "fixed",
				},
				TargetNode: "lattepanda", CPURequest: resource.MustParse("100m"),
			},
			Batch: 1,
			Protocol: edgev1alpha1.IdleProtocolSpec{
				AdmissionPolicy: "idle-wall-power-v1", SettlingDurationSeconds: 60, DurationSeconds: 30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
				},
			},
			PointPolicy: edgev1alpha1.ModelLoadedIdleBaselinePointPolicySpec{
				Version: "idle-point-v1", MinimumAcceptedRuns: 3, ConfidenceLevel: "0.95",
			},
			AttemptLimit: 5,
		},
	}
}
