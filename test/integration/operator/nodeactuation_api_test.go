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

var _ = Describe("NodeActuation API", func() {
	It("accepts one exact handoff and only the Apply-to-Restore desired transition", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("actuation-owner"))
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })
		handoff := validNodeActuation(uniqueNodeActuationName("transition"), run)
		Expect(k8sClient.Create(ctx, handoff)).To(Succeed())

		handoff.Spec.DesiredStage = edgev1alpha1.NodeActuationDesiredStageRestore
		handoff.Spec.StageRequestedAt = metav1.NewTime(handoff.Spec.StageRequestedAt.Add(time.Minute))
		Expect(k8sClient.Update(ctx, handoff)).To(Succeed())

		handoff.Spec.DesiredStage = edgev1alpha1.NodeActuationDesiredStageApply
		handoff.Spec.StageRequestedAt = metav1.NewTime(handoff.Spec.StageRequestedAt.Add(time.Minute))
		err := k8sClient.Update(ctx, handoff)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("rejects changing frozen handoff identity", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("actuation-immutable-owner"))
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })
		handoff := validNodeActuation(uniqueNodeActuationName("immutable"), run)
		Expect(k8sClient.Create(ctx, handoff)).To(Succeed())

		handoff.Spec.Node.UID = "other-node"
		err := k8sClient.Update(ctx, handoff)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	DescribeTable("rejects structurally unsafe handoffs",
		func(mutate func(*edgev1alpha1.NodeActuation)) {
			ctx := context.Background()
			run := validProfilingRun(uniqueProfilingRunName("actuation-invalid-owner"))
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })
			handoff := validNodeActuation(uniqueNodeActuationName("invalid"), run)
			mutate(handoff)

			err := k8sClient.Create(ctx, handoff)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("fixed target", func(handoff *edgev1alpha1.NodeActuation) {
			handoff.Spec.Actuator = edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorFixed}
		}),
		Entry("maximum does not exceed settling", func(handoff *edgev1alpha1.NodeActuation) {
			handoff.Spec.MaxActuationDurationSeconds = handoff.Spec.SettlingDurationSeconds
		}),
		Entry("deadline precedes request", func(handoff *edgev1alpha1.NodeActuation) {
			handoff.Spec.Deadline = metav1.NewTime(handoff.Spec.StageRequestedAt.Add(-time.Second))
		}),
	)

	It("accepts complete Ready status and rejects Failed without terminal failure", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("actuation-status-owner"))
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })
		handoff := validNodeActuation(uniqueNodeActuationName("status"), run)
		Expect(k8sClient.Create(ctx, handoff)).To(Succeed())

		now := metav1.Now()
		handoff.Status = edgev1alpha1.NodeActuationStatus{
			ObservedGeneration: handoff.Generation,
			Phase:              edgev1alpha1.NodeActuationPhaseReady,
			Reason:             "TargetSettled",
			Message:            "target was independently observed and settled",
			PreviousObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: "MODE_10W", Actuator: jetsonNVPModelActuator(1),
				BootID: "test-boot-id", ObservedAt: now,
			},
			TargetObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: handoff.Spec.PowerMode, Actuator: handoff.Spec.Actuator,
				BootID: "test-boot-id", ObservedAt: now,
			},
			ReadyAt: &now,
		}
		Expect(k8sClient.Status().Update(ctx, handoff)).To(Succeed())

		handoff.Status.Phase = edgev1alpha1.NodeActuationPhaseFailed
		handoff.Status.TerminalFailure = nil
		err := k8sClient.Status().Update(ctx, handoff)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("separates restore application from independent restore verification", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("actuation-restore-owner"))
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })
		handoff := validNodeActuation(uniqueNodeActuationName("restore"), run)
		Expect(k8sClient.Create(ctx, handoff)).To(Succeed())

		now := metav1.Now()
		handoff.Status = edgev1alpha1.NodeActuationStatus{
			ObservedGeneration: handoff.Generation,
			Phase:              edgev1alpha1.NodeActuationPhaseVerifyingRestore,
			PreviousObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: "MODE_10W", Actuator: jetsonNVPModelActuator(1),
				BootID: "test-boot-id", ObservedAt: now,
			},
			RestoreStartedAt: &now,
		}
		err := k8sClient.Status().Update(ctx, handoff)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())

		handoff.Status.RestoreAppliedAt = &now
		Expect(k8sClient.Status().Update(ctx, handoff)).To(Succeed())
	})

	It("requires exact blocking Pod identities for a blocked safety verdict", func() {
		ctx := context.Background()
		run := validProfilingRun(uniqueProfilingRunName("actuation-safety-owner"))
		Expect(k8sClient.Create(ctx, run)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, run) })
		handoff := validNodeActuation(uniqueNodeActuationName("safety"), run)
		Expect(k8sClient.Create(ctx, handoff)).To(Succeed())

		now := metav1.Now()
		handoff.Status.Safety = &edgev1alpha1.NodeActuationSafetyObservation{
			Verdict: edgev1alpha1.NodeActuationSafetyBlocked, ObservedAt: now,
		}
		err := k8sClient.Status().Update(ctx, handoff)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())

		handoff.Status.Safety.BlockingPods = []edgev1alpha1.NodeActuationPodReference{{
			Namespace: "tenant-a", Name: "serving", UID: "pod-uid",
		}}
		Expect(k8sClient.Status().Update(ctx, handoff)).To(Succeed())
	})
})

func validNodeActuation(name string, run *edgev1alpha1.ProfilingRun) *edgev1alpha1.NodeActuation {
	requested := time.Now().UTC().Truncate(time.Second)
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: run.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "ProfilingRun",
				Name: run.Name, UID: run.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: edgev1alpha1.NodeActuationSpec{
			Run:         edgev1alpha1.ProfilingRunObjectReference{Name: run.Name, UID: string(run.UID)},
			Node:        edgev1alpha1.ProfilingRunObjectReference{Name: "jetsonx", UID: "node-uid"},
			DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
			Actuator:                    jetsonNVPModelActuator(2),
			PolicyName:                  "jetson-mode-policy",
			SettlingDurationSeconds:     60,
			MaxActuationDurationSeconds: 900,
			Deadline:                    metav1.NewTime(requested.Add(15 * time.Minute)),
			DesiredStage:                edgev1alpha1.NodeActuationDesiredStageApply,
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
