package modelcatalog

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestFirstfloorExecutionRequiresSelectedNativeCondition(t *testing.T) {
	model := &edgev1alpha1.ModelSpec{Spec: edgev1alpha1.ModelSpecSpec{
		Artifacts: []edgev1alpha1.ModelArtifact{{Name: "model", Format: "onnx",
			Source: edgev1alpha1.ModelArtifactSource{Image: "artifact", Architecture: "amd64", Files: []string{"/artifact/model.onnx"}},
		}},
		ExecutionPaths: []edgev1alpha1.ModelExecutionPath{{Name: "native", Artifact: "model",
			Runtime: edgev1alpha1.ModelRuntimeRequirement{Family: "firstfloor", Backend: "ort-cpu"},
		}},
	}}
	if err := Validate(model); err == nil {
		t.Fatal("native execution path accepted unspecified native settings")
	}
	model.Spec.ExecutionPaths[0].Runtime.Settings = &edgev1alpha1.ModelRuntimeSettings{
		Firstfloor: &edgev1alpha1.FirstfloorRuntimeSettings{MaxBatchSize: 4, MaxQueueSize: 64},
	}
	if err := Validate(model); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"", "drop", "keep", "defer"} {
		runtime := &model.Spec.ExecutionPaths[0].Runtime
		runtime.Settings.Firstfloor.DeadlinePolicy = policy
		if err := Validate(model); err != nil {
			t.Fatalf("deadlinePolicy %q: %v", policy, err)
		}
		selected := FirstfloorSettings(*runtime)
		expected := policy
		if expected == "" {
			expected = "keep"
		}
		if selected.DeadlinePolicy != expected {
			t.Fatalf("deadlinePolicy %q mapped to %q", policy, selected.DeadlinePolicy)
		}
	}
	model.Spec.ExecutionPaths[0].Runtime.Settings.Firstfloor.DeadlinePolicy = "invalid"
	if err := Validate(model); err == nil {
		t.Fatal("invalid deadlinePolicy accepted")
	}
	model.Spec.ExecutionPaths[0].Runtime.Settings.Firstfloor.DeadlinePolicy = "keep"
	model.Spec.Artifacts[0].Format = "rknn"
	model.Spec.Artifacts[0].Input = &edgev1alpha1.ModelArtifactInputContract{Tensors: []edgev1alpha1.ModelInputTensor{{Name: "x", Datatype: "FP32", SampleShape: []int32{2}}}}
	if err := Validate(model); err == nil {
		t.Fatal("firstfloor ORT CPU path accepted a non-ONNX artifact")
	}
	model.Spec.Artifacts[0].Format = "onnx"
	if err := Validate(model); err == nil {
		t.Fatal("ONNX artifact accepted an input declaration")
	}
	model.Spec.Artifacts[0].Input = nil
	model.Spec.ExecutionPaths[0].Runtime.Family = "onnxruntime"
	if err := Validate(model); err == nil {
		t.Fatal("non-firstfloor runtime family accepted")
	}
}

func TestFirstfloorCatalogBackendArtifactRelation(t *testing.T) {
	for _, test := range []struct {
		name, format, backend string
		rknn                  bool
		wantError             bool
	}{
		{"ORT CPU", "onnx", "ort-cpu", false, false},
		{"ORT CUDA", "onnx", "ort-cuda", false, false},
		{"RKNN", "rknn", "rknnlite", true, false},
		{"RKNN through ORT", "rknn", "ort-cpu", true, true},
		{"ONNX through RKNN", "onnx", "rknnlite", false, true},
		{"unknown backend", "onnx", "python", false, true},
		{"RKNN lacks derivation", "rknn", "rknnlite", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := edgev1alpha1.ModelArtifact{
				Name: "model", Format: test.format,
				Source: edgev1alpha1.ModelArtifactSource{Image: "artifact", Architecture: "arm64", Files: []string{"/artifact/model." + test.format}},
			}
			if test.format == "rknn" {
				artifact.Input = &edgev1alpha1.ModelArtifactInputContract{Tensors: []edgev1alpha1.ModelInputTensor{
					{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}},
				}}
			}
			if test.rknn {
				artifact.Derivation = &edgev1alpha1.ModelArtifactDerivation{
					SourceArtifact: "source", TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{1, 2},
				}
			}
			source := artifact
			source.Name = "source"
			source.Derivation = nil
			model := &edgev1alpha1.ModelSpec{Spec: edgev1alpha1.ModelSpecSpec{
				Artifacts: []edgev1alpha1.ModelArtifact{source, artifact},
				ExecutionPaths: []edgev1alpha1.ModelExecutionPath{{
					Name: "native", Artifact: "model", Runtime: edgev1alpha1.ModelRuntimeRequirement{
						Family: "firstfloor", Backend: test.backend,
						Settings: &edgev1alpha1.ModelRuntimeSettings{Firstfloor: &edgev1alpha1.FirstfloorRuntimeSettings{MaxBatchSize: 4, MaxQueueSize: 32}},
					},
				}},
			}}
			if err := Validate(model); (err != nil) != test.wantError {
				t.Fatalf("Validate() = %v, want error %t", err, test.wantError)
			}
		})
	}
}

func TestArtifactSourceFiles(t *testing.T) {
	for _, test := range []struct {
		name      string
		files     []string
		wantError bool
	}{
		{"several files", []string{"/artifact/vision.onnx", "/artifact/decoder.onnx"}, false},
		{"no files", nil, true},
		{"relative", []string{"artifact/model.onnx"}, true},
		{"unclean", []string{"/artifact/../model.onnx"}, true},
		{"duplicate basename", []string{"/a/model.onnx", "/b/model.onnx"}, true},
		{"shared session stem", []string{"/artifact/model.onnx", "/artifact/model.onnx_data"}, true},
		{"dotted stems differ", []string{"/artifact/model.tar.gz", "/artifact/model.tar"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &edgev1alpha1.ModelSpec{Spec: edgev1alpha1.ModelSpecSpec{
				Artifacts: []edgev1alpha1.ModelArtifact{{Name: "model", Format: "onnx",
					Source: edgev1alpha1.ModelArtifactSource{Image: "artifact", Architecture: "amd64", Files: test.files},
				}},
			}}
			if err := Validate(model); (err != nil) != test.wantError {
				t.Fatalf("Validate() = %v, want error %t", err, test.wantError)
			}
		})
	}
}

func TestRKNNArtifactIsOneRKNNFile(t *testing.T) {
	for _, test := range []struct {
		name      string
		files     []string
		wantError bool
	}{
		{"one rknn file", []string{"/artifact/model.rknn"}, false},
		{"other extension", []string{"/artifact/model.onnx"}, true},
		{"two files", []string{"/artifact/model.rknn", "/artifact/extra.rknn"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &edgev1alpha1.ModelSpec{Spec: edgev1alpha1.ModelSpecSpec{
				Artifacts: []edgev1alpha1.ModelArtifact{{Name: "model", Format: "rknn",
					Source: edgev1alpha1.ModelArtifactSource{Image: "artifact", Architecture: "arm64", Files: test.files},
					Input: &edgev1alpha1.ModelArtifactInputContract{Tensors: []edgev1alpha1.ModelInputTensor{
						{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}},
					}},
				}},
			}}
			if err := Validate(model); (err != nil) != test.wantError {
				t.Fatalf("Validate() = %v, want error %t", err, test.wantError)
			}
		})
	}
}
