package operatorintegration

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

var _ = Describe("Workload API", func() {
	It("defaults omitted admission and validates overload policy and queue bounds", func() {
		ctx := context.Background()
		for _, admission := range []interface{}{nil, map[string]interface{}{}, map[string]interface{}{"policy": "Delay"}} {
			workload := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "edge.dacs.io/v1alpha1", "kind": "Workload",
				"metadata": map[string]interface{}{"name": uniqueModelSpecName("admission")},
				"spec": map[string]interface{}{
					"modelRef":        map[string]interface{}{"name": "model"},
					"preprocessorRef": map[string]interface{}{"name": "pre"},
					"slo":             map[string]interface{}{"percentile": float64(95), "targetLatency": "1s"},
				},
			}}
			if admission != nil {
				workload.Object["spec"].(map[string]interface{})["admission"] = admission
			}
			Expect(k8sClient.Create(ctx, workload)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, workload)).To(Succeed()) })
			policy, _, err := unstructured.NestedString(workload.Object, "spec", "admission", "policy")
			Expect(err).NotTo(HaveOccurred())
			if admission != nil && admission.(map[string]interface{})["policy"] == "Delay" {
				Expect(policy).To(Equal("Delay"))
			} else {
				Expect(policy).To(Equal("Reject"))
			}
			queued, _, err := unstructured.NestedInt64(workload.Object, "spec", "admission", "maxQueuedRequests")
			Expect(err).NotTo(HaveOccurred())
			Expect(queued).To(Equal(int64(1024)))
			for _, invalid := range []map[string]interface{}{
				{"policy": "Keep", "maxQueuedRequests": int64(1)},
				{"policy": "Reject", "maxQueuedRequests": int64(0)},
				{"policy": "Delay", "maxQueuedRequests": int64(-1)},
			} {
				changed := workload.DeepCopy()
				changed.Object["spec"].(map[string]interface{})["admission"] = invalid
				Expect(apierrors.IsInvalid(k8sClient.Update(ctx, changed))).To(BeTrue())
			}
		}
	})

	It("stores the first closed applied snapshot and rejects a stale status write", func() {
		ctx := context.Background()
		store, err := servingkube.NewStore(cfg, scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())
		workload := &edgev1alpha1.Workload{
			ObjectMeta: metav1.ObjectMeta{Name: uniqueModelSpecName("workload")},
			Spec: edgev1alpha1.WorkloadSpec{
				ModelRef:        edgev1alpha1.CatalogReference{Name: "model"},
				PreprocessorRef: edgev1alpha1.CatalogReference{Name: "preprocessor"},
				SLO:             edgev1alpha1.WorkloadSLO{Percentile: 99.5, TargetLatency: metav1.Duration{Duration: 50 * time.Millisecond}},
				Admission:       edgev1alpha1.DefaultWorkloadAdmission(),
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
				ModelRef:           edgev1alpha1.WorkloadAppliedCatalogReference{Name: "model", UID: "model-uid", Generation: 1},
				PreprocessorRef:    edgev1alpha1.WorkloadAppliedCatalogReference{Name: "preprocessor", UID: "preprocessor-uid", Generation: 1},
				SLO:                workload.Spec.SLO,
				Admission:          workload.Spec.Admission,
			},
			Admission: edgev1alpha1.WorkloadAdmissionStatus{Allowed: false, Reason: "MissingEvidence", ObservedAt: metav1.Now()},
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
