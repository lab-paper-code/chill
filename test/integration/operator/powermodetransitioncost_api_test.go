package operatorintegration

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var _ = Describe("PowerModeTransitionCost API", func() {
	It("accepts one immutable two-direction measurement", func() {
		ctx := context.Background()
		measurement := validPowerModeTransitionCost(uniquePowerModeTransitionCostName("valid"))

		Expect(k8sClient.Create(ctx, measurement)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, measurement) })
	})

	It("rejects changing an existing measurement spec", func() {
		ctx := context.Background()
		measurement := validPowerModeTransitionCost(uniquePowerModeTransitionCostName("immutable"))
		Expect(k8sClient.Create(ctx, measurement)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, measurement) })

		measurement.Spec.AttemptLimit++
		err := k8sClient.Update(ctx, measurement)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	DescribeTable("rejects invalid direction and count contracts",
		func(mutate func(*edgev1alpha1.PowerModeTransitionCost)) {
			ctx := context.Background()
			measurement := validPowerModeTransitionCost(uniquePowerModeTransitionCostName("invalid"))
			mutate(measurement)

			err := k8sClient.Create(ctx, measurement)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("one mode", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.Modes = measurement.Spec.Modes[:1]
		}),
		Entry("three modes", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.Modes = append(measurement.Spec.Modes, measurement.Spec.Modes[0])
		}),
		Entry("duplicate modes", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.Modes[1].PowerMode = measurement.Spec.Modes[0].PowerMode
		}),
		Entry("accepted target below five", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.AcceptedCountTarget = 4
		}),
		Entry("attempt limit below accepted target", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.AttemptLimit = measurement.Spec.AcceptedCountTarget - 1
		}),
		Entry("power timeout not below interval", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.ObservationPolicy.PowerTimeoutMilliseconds =
				measurement.Spec.ObservationPolicy.PowerIntervalMilliseconds
		}),
		Entry("zero fact poll interval", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.ObservationPolicy.FactPollIntervalMilliseconds = 0
		}),
		Entry("empty execution path", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.ExecutionPath = ""
		}),
		Entry("execution path longer than 128 characters", func(measurement *edgev1alpha1.PowerModeTransitionCost) {
			measurement.Spec.ExecutionPath = strings.Repeat("p", 129)
		}),
	)
})

func validPowerModeTransitionCost(name string) *edgev1alpha1.PowerModeTransitionCost {
	return &edgev1alpha1.PowerModeTransitionCost{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: edgev1alpha1.PowerModeTransitionCostSpec{
			TargetNode:       "test-node",
			DeviceClass:      "test-device-class",
			ModelSpec:        "test-model",
			ExecutionPath:    "ort-cpu",
			ServingService:   "test-service",
			ServingContainer: "test-container",
			Modes: []edgev1alpha1.PowerModeTransitionCostModeSpec{
				{PowerMode: "test-mode-a"},
				{PowerMode: "test-mode-b"},
			},
			AcceptedCountTarget: 5,
			AttemptLimit:        8,
			ObservationPolicy: edgev1alpha1.PowerModeTransitionCostObservationPolicySpec{
				PowerIntervalMilliseconds:                1000,
				PowerTimeoutMilliseconds:                 500,
				MaximumTransitionDurationSeconds:         900,
				FactPollIntervalMilliseconds:             50,
				MaximumBoundaryDistanceMilliseconds:      1000,
				MinimumCoverage:                          "0.95",
				MaximumSampleGapMilliseconds:             2000,
				MaximumBoundaryUncertaintyMilliseconds:   100,
				MaximumReadyObservationDelayMilliseconds: 5000,
			},
			EvidenceStore: "test-store",
		},
	}
}

func uniquePowerModeTransitionCostName(prefix string) string {
	return fmt.Sprintf("powermodetransitioncost-%s-%d", prefix, time.Now().UnixNano())
}
