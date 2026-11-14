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

		campaign.Spec.ActiveProtocol.WarmupIterations++
		err := k8sClient.Update(ctx, campaign)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("requires the session's operation observation settings", func() {
		ctx := context.Background()
		campaign := validBatchEnergyOptimization(uniqueBatchEnergyOptimizationName("session"))
		campaign.Spec.Session = validSession()
		Expect(k8sClient.Create(ctx, campaign)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, campaign) })

		missing := validBatchEnergyOptimization(uniqueBatchEnergyOptimizationName("session-unobserved"))
		missing.Spec.Session = validSession()
		missing.Spec.Session.Observation = edgev1alpha1.MeasurementObservationSpec{}
		err := k8sClient.Create(ctx, missing)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
		Expect(err.Error()).To(ContainSubstring("spec.session.observation"))
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
			},
			ActiveProtocol: edgev1alpha1.ActiveProtocolSpec{
				Workload:         "mobilenet",
				WarmupIterations: 20,
				DurationSeconds:  120,
				Dataset:          "vqav2-val-subset",
				OperatingPoints: edgev1alpha1.MeasurementOperatingPointsSpec{
					Utilizations: []float64{.5}, SparseMeanGapMilliseconds: 1000,
					PhaseDurationSeconds: 30, PostLoadDurationSeconds: 30, RequestLifetimeSeconds: 30,
					DeliveryMarginMilliseconds: 100, FramedDecodedRequestUpperBytes: 1024,
				},
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000,
					TimeoutMilliseconds:  500,
					DurationSeconds:      121,
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

func validSession() *edgev1alpha1.BatchEnergyOptimizationSessionSpec {
	return &edgev1alpha1.BatchEnergyOptimizationSessionSpec{
		MeterLagSeconds: 3, MinimumPowerWindowSeconds: 30, MidLevelRatio: .5, SparseMeanGapSeconds: 1,
		LevelDurationLimitSeconds: 600, PostLoadWindowSeconds: 120, ConfidenceLevel: "0.95",
		RequestLifetimeSeconds: 30, DeliveryMarginMilliseconds: 100, FramedDecodedRequestUpperBytes: 1024,
		Observation: edgev1alpha1.MeasurementObservationSpec{
			IntervalMilliseconds: 750, RequestTimeoutMilliseconds: 500, MaximumOperationSeconds: 600, MaximumGapMilliseconds: 3000,
			Timing: &edgev1alpha1.MeasurementTimingSpec{ChangeIntervalP95Seconds: "3", MaximumRequestSeconds: "0.5", MaximumReceiptGapSeconds: "0.1", Source: "pilot"},
		},
	}
}
