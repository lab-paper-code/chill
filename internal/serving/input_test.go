package serving

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"testing"

	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func TestValidateInputChecksRawContract(t *testing.T) {
	var picture bytes.Buffer
	if err := jpeg.Encode(&picture, image.NewGray(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	contract := []RawField{{Name: "image", Format: "JPEG", MaxBytes: 4096}, {Name: "question", Format: "UTF8", MaxBytes: 8}}
	sample := func(imageData, question []byte) *InferenceInput {
		return &InferenceInput{Inputs: []RawInput{{Name: "question", Datatype: "BYTES", Data: question}, {Name: "image", Datatype: "BYTES", Data: imageData}}}
	}
	if err := validateInput(sample(picture.Bytes(), []byte("why?")), contract); err != nil {
		t.Fatalf("valid sample rejected: %v", err)
	}
	for name, tc := range map[string]struct {
		input *InferenceInput
		field string
	}{
		"missing field":   {&InferenceInput{Inputs: []RawInput{{Name: "image", Datatype: "BYTES", Data: picture.Bytes()}}}, "inputs"},
		"unknown field":   {&InferenceInput{Inputs: []RawInput{{Name: "image", Datatype: "BYTES", Data: picture.Bytes()}, {Name: "other", Datatype: "BYTES", Data: []byte("a")}}}, "inputs"},
		"duplicate field": {&InferenceInput{Inputs: []RawInput{{Name: "image", Datatype: "BYTES", Data: picture.Bytes()}, {Name: "image", Datatype: "BYTES", Data: picture.Bytes()}}}, "inputs"},
		"not BYTES":       {&InferenceInput{Inputs: []RawInput{{Name: "question", Datatype: "FP32", Data: []byte("a")}, {Name: "image", Datatype: "BYTES", Data: picture.Bytes()}}}, "inputs[0].datatype"},
		"empty":           {sample(picture.Bytes(), nil), "inputs[0].data"},
		"over maxBytes":   {sample(picture.Bytes(), []byte("123456789")), "inputs[0].data"},
		"bad UTF-8":       {sample(picture.Bytes(), []byte{0xff, 0xfe}), "inputs[0].data"},
		"not JPEG":        {sample([]byte("GIF89a"), []byte("why?")), "inputs[1].data"},
		"bad JPEG header": {sample([]byte{0xFF, 0xD8, 0xFF, 0x00, 0x01}, []byte("why?")), "inputs[1].data"},
	} {
		var domain *Error
		if err := validateInput(tc.input, contract); !errors.As(err, &domain) || domain.Code != "InvalidInput" || domain.Field != tc.field {
			t.Errorf("%s: got %v (%+v), want InvalidInput at %s", name, err, domain, tc.field)
		}
	}
}

func TestProfilePathRequiresAppliedPreprocessorAndDataset(t *testing.T) {
	_, snapshot := schedulerFixture()
	d, measured := snapshot.Workloads[0], *snapshot.Instances[0].Profile
	if profilePath(d, &measured) == nil {
		t.Fatal("fixture profile must match its definition")
	}
	for name, mutate := range map[string]func(*Definition, *Profile){
		"preprocessor image": func(_ *Definition, p *Profile) { p.Condition.Preprocessor.Image = "other@sha256:1" },
		"preprocessor name":  func(d *Definition, _ *Profile) { d.Preprocessor.Name = "other" },
		"output fields":      func(d *Definition, _ *Profile) { d.Preprocessor.Output = []RawField{{Name: "z"}} },
		"max new tokens":     func(d *Definition, _ *Profile) { d.Preprocessor.MaxNewTokens = 8 },
		"dataset UID": func(_ *Definition, p *Profile) {
			p.Condition.ActiveProtocol.Dataset = &derivation.CurveDatasetContract{Name: "data", UID: "other"}
		},
		"no applied dataset":  func(d *Definition, _ *Profile) { d.Dataset = nil },
		"no measured dataset": func(_ *Definition, p *Profile) { p.Condition.ActiveProtocol.Dataset = nil },
	} {
		definition, profile := *d, measured
		mutate(&definition, &profile)
		if profilePath(&definition, &profile) != nil {
			t.Errorf("%s mismatch matched the profile", name)
		}
	}
}
