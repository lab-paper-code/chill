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

var _ = Describe("BatchEnergyOptimization API", func() {
	It("accepts one minimum immutable campaign request", func() {
		ctx := context.Background()
		campaign := validBatchEnergyOptimization(uniqueBatchEnergyOptimizationName("valid"))

		Expect(k8sClient.Create(ctx, campaign)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, campaign) })
	})

	It("rejects changing an existing campaign spec", func() {
		ctx := context.Background()
		campaign := validBatchEnergyOptimization(uniqueBatchEnergyOptimizationName("immutable"))
		Expect(k8sClient.Create(ctx, campaign)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, campaign) })

		campaign.Spec.RetryLimit++
		err := k8sClient.Update(ctx, campaign)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})
})

func uniqueBatchEnergyOptimizationName(prefix string) string {
	return fmt.Sprintf("batchenergyoptimization-%s-%d", prefix, time.Now().UnixNano())
}

func validBatchEnergyOptimization(name string) *edgev1alpha1.BatchEnergyOptimization {
	return &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec:     "mobilenet-v2-050",
					DeviceClass:   "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu",
					Preprocessor:  "mobilenet-imagenet",
					PowerMode:     "fixed",
				},
				TargetNode: "lattepanda",
				CPURequest: resource.MustParse("100m"),
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{
				WarmupIterations: 20,
				DurationSeconds:  30,
				Dataset:          "vqav2-val-subset",
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000,
					TimeoutMilliseconds:  500,
					DurationSeconds:      31,
				},
			},
			IdleProtocol: edgev1alpha1.IdleProtocolSpec{
				SettlingDurationSeconds: 60,
				DurationSeconds:         30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000,
					TimeoutMilliseconds:  500,
					DurationSeconds:      31,
				},
			},
			TrialsPerBatch: 3,
			RetryLimit:     2,
		},
	}
}
