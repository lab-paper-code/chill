package modelcatalog

import (
	"slices"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestPreprocessorRejectsDuplicateTarget(t *testing.T) {
	artifact := edgev1alpha1.PreprocessorArtifact{
		Target: edgev1alpha1.PreprocessorTarget{Model: "smolvlm2", Artifact: "onnx-fp32"},
		Source: edgev1alpha1.PreprocessorArtifactSource{Image: "carrier", Architecture: "arm64", Path: "/artifact/smolvlm2-vqa"},
	}
	p := &edgev1alpha1.Preprocessor{Spec: edgev1alpha1.PreprocessorSpec{
		Input:     edgev1alpha1.RawInputContract{Fields: []edgev1alpha1.RawField{{Name: "image", Format: "JPEG", MaxBytes: 4096}}},
		Output:    edgev1alpha1.RawOutputContract{Fields: []edgev1alpha1.RawOutputField{{Name: "answer", Format: "UTF8"}}},
		Artifacts: []edgev1alpha1.PreprocessorArtifact{artifact},
	}}
	if err := ValidatePreprocessor(p); err != nil {
		t.Fatal(err)
	}
	p.Spec.Artifacts = append(p.Spec.Artifacts, artifact)
	if err := ValidatePreprocessor(p); err == nil {
		t.Fatal("duplicate target model artifact accepted")
	}
}

func TestCompatibleReportsUnsatisfiedInputFields(t *testing.T) {
	p := &edgev1alpha1.Preprocessor{Spec: edgev1alpha1.PreprocessorSpec{Input: edgev1alpha1.RawInputContract{Fields: []edgev1alpha1.RawField{
		{Name: "image", Format: "JPEG", MaxBytes: 4096},
		{Name: "question", Format: "UTF8", MaxBytes: 1024},
		{Name: "caption", Format: "UTF8", MaxBytes: 1024},
	}}}}
	d := &edgev1alpha1.Dataset{Spec: edgev1alpha1.DatasetSpec{Fields: []edgev1alpha1.RawField{
		{Name: "image", Format: "JPEG", MaxBytes: 4096},
		{Name: "question", Format: "UTF8", MaxBytes: 2048},
		{Name: "caption", Format: "JPEG", MaxBytes: 16},
		{Name: "answer", Format: "UTF8", MaxBytes: 16},
	}}}
	if got := Compatible(d, p); !slices.Equal(got, []string{"question", "caption"}) {
		t.Fatalf("mismatched fields = %v", got)
	}
	p.Spec.Input.Fields = p.Spec.Input.Fields[:1]
	if got := Compatible(d, p); len(got) != 0 {
		t.Fatalf("subset contract reported mismatches %v", got)
	}
}
