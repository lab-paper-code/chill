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

var _ = Describe("BatchSaturation API", func() {
	It("accepts one minimum immutable campaign request", func() {
		ctx := context.Background()
		campaign := validBatchSaturation(uniqueBatchSaturationName("valid"))

		Expect(k8sClient.Create(ctx, campaign)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, campaign) })
	})

	DescribeTable("rejects invalid admission policy selection",
		func(mutate func(*edgev1alpha1.BatchSaturation)) {
			ctx := context.Background()
			campaign := validBatchSaturation(uniqueBatchSaturationName("invalid"))
			mutate(campaign)

			err := k8sClient.Create(ctx, campaign)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("empty active policy", func(campaign *edgev1alpha1.BatchSaturation) {
			campaign.Spec.ActiveProtocol.AdmissionPolicy = ""
		}),
		Entry("malformed active policy name", func(campaign *edgev1alpha1.BatchSaturation) {
			campaign.Spec.ActiveProtocol.AdmissionPolicy = invalidAdmissionPolicy
		}),
		Entry("empty idle policy", func(campaign *edgev1alpha1.BatchSaturation) {
			campaign.Spec.IdleProtocol.AdmissionPolicy = ""
		}),
		Entry("malformed idle policy name", func(campaign *edgev1alpha1.BatchSaturation) {
			campaign.Spec.IdleProtocol.AdmissionPolicy = invalidAdmissionPolicy
		}),
		Entry("one policy cannot serve both workload modes", func(campaign *edgev1alpha1.BatchSaturation) {
			campaign.Spec.IdleProtocol.AdmissionPolicy = campaign.Spec.ActiveProtocol.AdmissionPolicy
		}),
	)

	It("rejects changing an existing campaign spec", func() {
		ctx := context.Background()
		campaign := validBatchSaturation(uniqueBatchSaturationName("immutable"))
		Expect(k8sClient.Create(ctx, campaign)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, campaign) })

		campaign.Spec.RetryLimit++
		err := k8sClient.Update(ctx, campaign)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})
})

func uniqueBatchSaturationName(prefix string) string {
	return fmt.Sprintf("batchsaturation-%s-%d", prefix, time.Now().UnixNano())
}

func validBatchSaturation(name string) *edgev1alpha1.BatchSaturation {
	return &edgev1alpha1.BatchSaturation{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: edgev1alpha1.BatchSaturationSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec:     "mobilenet-v2-050",
					DeviceClass:   "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu",
					PowerMode:     "fixed",
				},
				TargetNode: "lattepanda",
				CPURequest: resource.MustParse("100m"),
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{
				AdmissionPolicy:  "wall-power-v2",
				WarmupIterations: 20,
				DurationSeconds:  30,
				InputGenerator:   "numpy-default-rng-seed-0-fp32-v1",
				OutputValidation: "nonempty-shape-batch-v1",
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000,
					TimeoutMilliseconds:  500,
					DurationSeconds:      31,
				},
			},
			IdleProtocol: edgev1alpha1.IdleProtocolSpec{
				AdmissionPolicy:         idleAdmissionPolicy,
				SettlingDurationSeconds: 60,
				DurationSeconds:         30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000,
					TimeoutMilliseconds:  500,
					DurationSeconds:      31,
				},
			},
			Method: edgev1alpha1.MeasurementMethodSpec{
				Name: edgev1alpha1.MethodSequentialEarlyStop,
				SequentialEarlyStop: &edgev1alpha1.SequentialEarlyStopSpec{
					MinDelta:         "0.05",
					Patience:         1,
					TrialsPerBatch:   3,
					DivergenceFactor: "1.5",
				},
			},
			RetryLimit: 2,
		},
	}
}
