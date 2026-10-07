package modelcatalog

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestTritonExecutionRequiresSelectedNativeCondition(t *testing.T) {
	model := &edgev1alpha1.ModelSpec{Spec: edgev1alpha1.ModelSpecSpec{
		Artifacts: []edgev1alpha1.ModelArtifact{{Name: "model", Format: "onnx",
			Source: edgev1alpha1.ModelArtifactSource{Image: "artifact", Architecture: "amd64", Path: "/artifact/model.onnx"},
			Input:  &edgev1alpha1.ModelArtifactInputContract{Tensors: []edgev1alpha1.ModelInputTensor{{Name: "x", Datatype: "FP32", SampleShape: []int32{2}}}},
		}},
		ExecutionPaths: []edgev1alpha1.ModelExecutionPath{{Name: "native", Artifact: "model",
			Runtime: edgev1alpha1.ModelRuntimeRequirement{Family: "triton", Backend: "python"},
		}},
	}}
	if err := Validate(model); err == nil {
		t.Fatal("native execution path accepted unspecified native settings")
	}
	model.Spec.ExecutionPaths[0].Runtime.Settings = &edgev1alpha1.ModelRuntimeSettings{
		Triton: &edgev1alpha1.TritonRuntimeSettings{MaxBatchSize: 4, PriorityLevels: 2, MaxQueueSize: 64},
	}
	if err := Validate(model); err != nil {
		t.Fatal(err)
	}
	model.Spec.Artifacts[0].Format = "rknn"
	if err := Validate(model); err == nil {
		t.Fatal("Triton Python CPU path accepted a non-ONNX artifact")
	}
	model.Spec.Artifacts[0].Format = "onnx"
	model.Spec.ExecutionPaths[0].Runtime.Family = "onnxruntime"
	if err := Validate(model); err == nil {
		t.Fatal("native settings were silently ignored by standalone ORT")
	}
}
