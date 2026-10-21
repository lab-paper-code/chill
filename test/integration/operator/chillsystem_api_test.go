package operatorintegration

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var _ = Describe("ChillSystem API", func() {
	It("preserves Evidence Store desired state", func() {
		ctx := context.Background()
		name := fmt.Sprintf("chillsystem-evidence-store-%d", time.Now().UnixNano())
		system := &edgev1alpha1.ChillSystem{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: edgev1alpha1.ChillSystemSpec{
				EvidenceStore: edgev1alpha1.ChillEvidenceStoreSpec{
					Enabled:   true,
					ConfigRef: corev1.LocalObjectReference{Name: "chill-evidence-store-config"},
				},
			},
		}

		Expect(k8sClient.Create(ctx, system)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, system) })

		stored := &edgev1alpha1.ChillSystem{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, stored)).To(Succeed())
		Expect(stored.Spec.EvidenceStore.Enabled).To(BeTrue())
		Expect(stored.Spec.EvidenceStore.ConfigRef.Name).To(Equal("chill-evidence-store-config"))
	})

	It("rejects an enabled Evidence Store without a configuration reference", func() {
		ctx := context.Background()
		system := &edgev1alpha1.ChillSystem{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("chillsystem-evidence-store-invalid-%d", time.Now().UnixNano()),
			},
			Spec: edgev1alpha1.ChillSystemSpec{
				EvidenceStore: edgev1alpha1.ChillEvidenceStoreSpec{Enabled: true},
			},
		}

		Expect(k8sClient.Create(ctx, system)).NotTo(Succeed())
	})

	It("preserves Profiler desired state and component conditions", func() {
		ctx := context.Background()
		name := fmt.Sprintf("chillsystem-profiler-%d", time.Now().UnixNano())
		system := &edgev1alpha1.ChillSystem{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: edgev1alpha1.ChillSystemSpec{
				Profiler: edgev1alpha1.ChillProfilerSpec{
					Enabled:   true,
					ConfigRef: corev1.LocalObjectReference{Name: "chill-profiler-config"},
				},
			},
		}

		Expect(k8sClient.Create(ctx, system)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, system) })

		stored := &edgev1alpha1.ChillSystem{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, stored)).To(Succeed())
		Expect(stored.Spec.Profiler.Enabled).To(BeTrue())
		Expect(stored.Spec.Profiler.ConfigRef.Name).To(Equal("chill-profiler-config"))

		stored.Status.ObservedGeneration = stored.Generation
		stored.Status.Conditions = []metav1.Condition{
			{
				Type:               edgev1alpha1.ChillSystemConditionProfilerAvailable,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: stored.Generation,
				LastTransitionTime: metav1.Now(),
				Reason:             "ProfilerReady",
				Message:            "Profiler Deployment is available",
			},
			{
				Type:               edgev1alpha1.ChillSystemConditionReady,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: stored.Generation,
				LastTransitionTime: metav1.Now(),
				Reason:             "ComponentsAvailable",
				Message:            "all enabled components are available",
			},
		}
		Expect(k8sClient.Status().Update(ctx, stored)).To(Succeed())

		observed := &edgev1alpha1.ChillSystem{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, observed)).To(Succeed())
		Expect(observed.Status.Conditions).To(HaveLen(2))
	})

	It("requires and preserves the node agent config reference", func() {
		ctx := context.Background()
		name := fmt.Sprintf("chillsystem-node-agent-%d", time.Now().UnixNano())
		system := &edgev1alpha1.ChillSystem{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: edgev1alpha1.ChillSystemSpec{NodeAgent: edgev1alpha1.ChillNodeAgentSpec{
				Enabled: true, Actuation: true, ConfigRef: corev1.LocalObjectReference{Name: "chill-node-agent-config"},
			}},
		}
		Expect(k8sClient.Create(ctx, system)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, system) })

		stored := &edgev1alpha1.ChillSystem{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, stored)).To(Succeed())
		Expect(stored.Spec.NodeAgent.Enabled).To(BeTrue())
		Expect(stored.Spec.NodeAgent.Actuation).To(BeTrue())
		Expect(stored.Spec.NodeAgent.ConfigRef.Name).To(Equal("chill-node-agent-config"))

		for _, spec := range []edgev1alpha1.ChillNodeAgentSpec{
			{Enabled: true},
			{Actuation: true, ConfigRef: corev1.LocalObjectReference{Name: "chill-node-agent-config"}},
		} {
			invalid := &edgev1alpha1.ChillSystem{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("chillsystem-node-agent-invalid-%d", time.Now().UnixNano())},
				Spec:       edgev1alpha1.ChillSystemSpec{NodeAgent: spec},
			}
			Expect(k8sClient.Create(ctx, invalid)).NotTo(Succeed())
		}
	})
})
