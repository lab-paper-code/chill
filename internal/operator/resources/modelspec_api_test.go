package resources

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

var _ = Describe("ModelSpec API", func() {
	It("accepts the minimum CPU ORT catalog shape", func() {
		ctx := context.Background()
		model := validModelSpec(uniqueModelSpecName("valid"))

		Expect(k8sClient.Create(ctx, model)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, model)
		})
	})

	DescribeTable("rejects structurally invalid catalog fields",
		func(mutate func(*edgev1alpha1.ModelSpec)) {
			ctx := context.Background()
			model := validModelSpec(uniqueModelSpecName("invalid"))
			mutate(model)

			err := k8sClient.Create(ctx, model)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
		},
		Entry("empty artifact list", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts = nil
		}),
		Entry("empty execution-path list", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.ExecutionPaths = nil
		}),
		Entry("non-canonical artifact digest", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Digest = "8645"
		}),
		Entry("uppercase artifact digest", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Digest = "sha256:" + "A" + model.Spec.Artifacts[0].Digest[8:]
		}),
		Entry("empty artifact name", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Name = ""
		}),
		Entry("empty artifact format", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Format = ""
		}),
		Entry("duplicate artifact name", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts = append(model.Spec.Artifacts, model.Spec.Artifacts[0])
		}),
		Entry("empty execution-path name", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.ExecutionPaths[0].Name = ""
		}),
		Entry("empty artifact reference", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.ExecutionPaths[0].Artifact = ""
		}),
		Entry("duplicate execution-path name", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.ExecutionPaths = append(model.Spec.ExecutionPaths, model.Spec.ExecutionPaths[0])
		}),
		Entry("empty runtime family", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.ExecutionPaths[0].Runtime.Family = ""
		}),
		Entry("empty runtime backend", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.ExecutionPaths[0].Runtime.Backend = ""
		}),
	)

	It("leaves artifact-reference resolution outside OpenAPI validation", func() {
		ctx := context.Background()
		model := validModelSpec(uniqueModelSpecName("dangling-reference"))
		model.Spec.ExecutionPaths[0].Artifact = "missing"

		Expect(k8sClient.Create(ctx, model)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, model)
		})
	})

	It("leaves runtime vocabulary support outside OpenAPI validation", func() {
		ctx := context.Background()
		model := validModelSpec(uniqueModelSpecName("unknown-vocabulary"))
		model.Spec.Artifacts[0].Format = "future-format"
		model.Spec.ExecutionPaths[0].Runtime.Family = "future-runtime"
		model.Spec.ExecutionPaths[0].Runtime.Backend = "future-backend"

		Expect(k8sClient.Create(ctx, model)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, model)
		})
	})
})

func uniqueModelSpecName(prefix string) string {
	return fmt.Sprintf("modelspec-%s-%d", prefix, time.Now().UnixNano())
}

func validModelSpec(name string) *edgev1alpha1.ModelSpec {
	return &edgev1alpha1.ModelSpec{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: edgev1alpha1.ModelSpecSpec{
			Artifacts: []edgev1alpha1.ModelArtifact{
				{
					Name:   "canonical-onnx",
					Format: "onnx",
					Digest: "sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2",
				},
			},
			ExecutionPaths: []edgev1alpha1.ModelExecutionPath{
				{
					Name:     "ort-cpu",
					Artifact: "canonical-onnx",
					Runtime: edgev1alpha1.ModelRuntimeRequirement{
						Family:  "onnxruntime",
						Backend: "CPUExecutionProvider",
					},
				},
			},
		},
	}
}
