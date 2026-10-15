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

var _ = Describe("ModelSpec API", func() {
	It("accepts the minimum CPU ORT catalog shape", func() {
		ctx := context.Background()
		model := validModelSpec(uniqueModelSpecName("valid"))

		Expect(k8sClient.Create(ctx, model)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, model)
		})
	})

	It("accepts a derived artifact", func() {
		ctx := context.Background()
		model := validModelSpec(uniqueModelSpecName("derived"))
		model.Spec.Artifacts = append(model.Spec.Artifacts, edgev1alpha1.ModelArtifact{
			Name:   "mobilenet-v2-050-rk3588-bs1",
			Format: "rknn",
			Source: edgev1alpha1.ModelArtifactSource{
				Image: "registry.example/model-rknn@sha256:digest", Architecture: "arm64",
				Path: "/artifact/model.rknn",
			},
			Input: &edgev1alpha1.ModelArtifactInputContract{
				Tensors: []edgev1alpha1.ModelInputTensor{{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}}},
			},
			Derivation: validDerivedArtifact(),
		})

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
		Entry("empty artifact name", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Name = ""
		}),
		Entry("empty artifact format", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Format = ""
		}),
		Entry("empty artifact source image", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Source.Image = ""
		}),
		Entry("empty artifact source architecture", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Source.Architecture = ""
		}),
		Entry("relative artifact source path", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Source.Path = "artifact/model.onnx"
		}),
		Entry("missing artifact input contract", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Input = nil
		}),
		Entry("duplicate artifact name", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts = append(model.Spec.Artifacts, model.Spec.Artifacts[0])
		}),
		Entry("empty derived source artifact", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Derivation = validDerivedArtifact()
			model.Spec.Artifacts[0].Derivation.SourceArtifact = ""
		}),
		Entry("empty derived target platform", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Derivation = validDerivedArtifact()
			model.Spec.Artifacts[0].Derivation.TargetPlatform = ""
		}),
		Entry("zero declared batch size", func(model *edgev1alpha1.ModelSpec) {
			model.Spec.Artifacts[0].Derivation = validDerivedArtifact()
			model.Spec.Artifacts[0].Derivation.DeclaredBatchSizes = []int32{0}
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

func validDerivedArtifact() *edgev1alpha1.ModelArtifactDerivation {
	return &edgev1alpha1.ModelArtifactDerivation{
		SourceArtifact:     "canonical-onnx",
		TargetPlatform:     "rk3588",
		DeclaredBatchSizes: []int32{1},
		Conversion: edgev1alpha1.ModelArtifactConversion{
			Tool:    "rknn-toolkit2",
			Version: "2.3.2",
		},
	}
}

func validModelSpec(name string) *edgev1alpha1.ModelSpec {
	return &edgev1alpha1.ModelSpec{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: edgev1alpha1.ModelSpecSpec{
			Artifacts: []edgev1alpha1.ModelArtifact{
				{
					Name:   "canonical-onnx",
					Format: "onnx",
					Source: edgev1alpha1.ModelArtifactSource{
						Image: "registry.example/model@sha256:digest", Architecture: "arm64",
						Path: "/artifact/model.onnx",
					},
					Input: &edgev1alpha1.ModelArtifactInputContract{
						Tensors: []edgev1alpha1.ModelInputTensor{{Name: "input", Datatype: "FP32", SampleShape: []int32{3, 224, 224}}},
					},
				},
			},
			ExecutionPaths: []edgev1alpha1.ModelExecutionPath{
				{
					Name:     "ort-cpu",
					Artifact: "canonical-onnx",
					Runtime: edgev1alpha1.ModelRuntimeRequirement{
						Family:  "firstfloor",
						Backend: "ort-cpu",
						Settings: &edgev1alpha1.ModelRuntimeSettings{
							Firstfloor: &edgev1alpha1.FirstfloorRuntimeSettings{MaxBatchSize: 4, MaxQueueSize: 32},
						},
					},
				},
			},
		},
	}
}
