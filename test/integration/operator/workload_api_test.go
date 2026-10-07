package operatorintegration

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

var _ = Describe("Workload API", func() {
	It("stores the first closed applied snapshot and rejects a stale status write", func() {
		ctx := context.Background()
		store, err := servingkube.NewStore(cfg, scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())
		workload := &edgev1alpha1.Workload{
			ObjectMeta: metav1.ObjectMeta{Name: uniqueModelSpecName("workload")},
			Spec: edgev1alpha1.WorkloadSpec{
				ModelRef: edgev1alpha1.WorkloadModelReference{Name: "model"},
				SLO:      edgev1alpha1.WorkloadSLO{Percentile: 99.5, TargetLatency: metav1.Duration{Duration: 50 * time.Millisecond}},
			},
		}
		Expect(store.CreateWorkload(ctx, workload)).To(Succeed())
		DeferCleanup(func() {
			Expect(store.DeleteWorkload(ctx, workload)).To(Succeed())
			deleting, err := store.GetWorkload(ctx, workload.Name)
			Expect(err).NotTo(HaveOccurred())
			Expect(store.RemoveWorkloadFinalizer(ctx, deleting)).To(Succeed())
		})
		status := edgev1alpha1.WorkloadStatus{
			AppliedDefinition: &edgev1alpha1.WorkloadAppliedDefinition{
				WorkloadGeneration: workload.Generation,
				ModelRef:           edgev1alpha1.WorkloadAppliedModelReference{Name: "model", UID: "model-uid", Generation: 1},
				SLO:                workload.Spec.SLO,
			},
			Admission: edgev1alpha1.WorkloadAdmission{Allowed: false, Reason: "MissingEvidence", ObservedAt: metav1.Now()},
		}
		updated, err := store.UpdateWorkloadStatus(ctx, workload, status)
		Expect(err).NotTo(HaveOccurred())
		persisted, err := store.GetWorkload(ctx, workload.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted.Status.Admission.Allowed).To(BeFalse())
		Expect(persisted.Status.Admission.Reason).To(Equal("MissingEvidence"))
		Expect(persisted.Status.AppliedDefinition.SLO.Percentile).To(Equal(99.5))
		_, err = store.UpdateWorkloadStatus(ctx, workload, status)
		Expect(apierrors.IsConflict(err)).To(BeTrue())
		workload = updated
	})
})
