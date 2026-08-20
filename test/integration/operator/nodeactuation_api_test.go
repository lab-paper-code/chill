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

var _ = Describe("NodeActuation API", func() {
	It("accepts one exact transition and rejects transition-request mutation", func() {
		ctx := context.Background()
		transition := validNodeActuation(uniqueNodeActuationName("transition"))
		Expect(k8sClient.Create(ctx, transition)).To(Succeed())

		transition.Spec.StageRequestedAt = metav1.NewTime(transition.Spec.StageRequestedAt.Add(time.Minute))
		err := k8sClient.Update(ctx, transition)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("rejects changing frozen transition identity", func() {
		ctx := context.Background()
		transition := validNodeActuation(uniqueNodeActuationName("immutable"))
		Expect(k8sClient.Create(ctx, transition)).To(Succeed())

		transition.Spec.Node.UID = "other-node"
		err := k8sClient.Update(ctx, transition)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	DescribeTable("rejects structurally unsafe transitions",
		func(mutate func(*edgev1alpha1.NodeActuation)) {
			ctx := context.Background()
			transition := validNodeActuation(uniqueNodeActuationName("invalid"))
			mutate(transition)

			err := k8sClient.Create(ctx, transition)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("fixed target", func(transition *edgev1alpha1.NodeActuation) {
			transition.Spec.Actuator = edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorFixed}
		}),
		Entry("maximum does not exceed settling", func(transition *edgev1alpha1.NodeActuation) {
			transition.Spec.MaxActuationDurationSeconds = transition.Spec.SettlingDurationSeconds
		}),
		Entry("deadline precedes request", func(transition *edgev1alpha1.NodeActuation) {
			transition.Spec.Deadline = metav1.NewTime(transition.Spec.StageRequestedAt.Add(-time.Second))
		}),
	)

	It("accepts complete Ready status and rejects Failed without terminal failure", func() {
		ctx := context.Background()
		transition := validNodeActuation(uniqueNodeActuationName("status"))
		Expect(k8sClient.Create(ctx, transition)).To(Succeed())

		now := metav1.Now()
		observedAt := metav1.NewMicroTime(now.Time)
		readyAt := metav1.NewMicroTime(now.Time)
		transition.Status = edgev1alpha1.NodeActuationStatus{
			ObservedGeneration: transition.Generation,
			Phase:              edgev1alpha1.NodeActuationPhaseReady,
			Reason:             "TargetSettled",
			Message:            "target was independently observed and settled",
			PreviousObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: "MODE_10W", Actuator: jetsonNVPModelActuator(1), ObservedAt: observedAt,
			},
			TargetObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: transition.Spec.PowerMode, Actuator: transition.Spec.Actuator, ObservedAt: observedAt,
			},
			ReadyAt: &readyAt,
		}
		Expect(k8sClient.Status().Update(ctx, transition)).To(Succeed())

		transition.Status.Phase = edgev1alpha1.NodeActuationPhaseFailed
		transition.Status.TerminalFailure = nil
		err := k8sClient.Status().Update(ctx, transition)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("requires exact blocking Pod identities for a blocked safety verdict", func() {
		ctx := context.Background()
		transition := validNodeActuation(uniqueNodeActuationName("safety"))
		Expect(k8sClient.Create(ctx, transition)).To(Succeed())

		now := metav1.Now()
		transition.Status.Safety = &edgev1alpha1.NodeActuationSafetyObservation{
			Verdict: edgev1alpha1.NodeActuationSafetyBlocked, ObservedAt: now,
		}
		err := k8sClient.Status().Update(ctx, transition)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())

		transition.Status.Safety.BlockingPods = []edgev1alpha1.NodeActuationPodReference{{
			Namespace: "tenant-a", Name: "serving", UID: "pod-uid",
		}}
		Expect(k8sClient.Status().Update(ctx, transition)).To(Succeed())
	})
})

func validNodeActuation(name string) *edgev1alpha1.NodeActuation {
	requested := time.Now().UTC().Truncate(time.Second)
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: edgev1alpha1.NodeActuationSpec{
			Node:        edgev1alpha1.ProfilingRunObjectReference{Name: "jetsonx", UID: "node-uid"},
			DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
			Actuator:                    jetsonNVPModelActuator(2),
			PolicyName:                  "jetson-mode-policy",
			SettlingDurationSeconds:     60,
			MaxActuationDurationSeconds: 900,
			Deadline:                    metav1.NewTime(requested.Add(15 * time.Minute)),
			StageRequestedAt:            metav1.NewTime(requested),
		},
	}
}

func jetsonNVPModelActuator(id int32) edgev1alpha1.PowerModeActuator {
	return edgev1alpha1.PowerModeActuator{
		Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
		JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: id},
	}
}

func uniqueNodeActuationName(prefix string) string {
	return fmt.Sprintf("nodeactuation-%s-%d", prefix, time.Now().UnixNano())
}
