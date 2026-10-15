package serving

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG header decoder for image.DecodeConfig
	"unicode/utf8"
)

// InferenceInput is one raw sample. The batch axis is added only by the
// backend adapter; external callers cannot override the registered model or SLO.
type InferenceInput struct {
	Inputs []RawInput `json:"inputs"`
}

// RawInput is one raw sample field. encoding/json decodes Data from base64.
type RawInput struct {
	Name     string `json:"name"`
	Datatype string `json:"datatype"`
	Data     []byte `json:"data"`
}

// validateInput checks a sample once against the applied raw input contract,
// which every execution path of the Workload shares. Pixel decoding belongs to
// the runner's preprocessing.
func validateInput(input *InferenceInput, contract []RawField) error {
	if input == nil || len(input.Inputs) != len(contract) {
		return inputFailure("inputs", "inputs must be exactly the fields %v", rawFieldNames(contract))
	}
	for i, field := range input.Inputs {
		var expected *RawField
		for n := range contract {
			if contract[n].Name == field.Name {
				expected = &contract[n]
			}
		}
		if expected == nil {
			return inputFailure("inputs", "inputs must be exactly the fields %v", rawFieldNames(contract))
		}
		for _, other := range input.Inputs[:i] {
			if other.Name == field.Name {
				return inputFailure("inputs", "input field %q appears more than once", field.Name)
			}
		}
		at := fmt.Sprintf("inputs[%d].data", i)
		if field.Datatype != "BYTES" {
			return inputFailure(fmt.Sprintf("inputs[%d].datatype", i), "input field %q datatype must be BYTES", field.Name)
		}
		if len(field.Data) == 0 || int64(len(field.Data)) > int64(expected.MaxBytes) {
			return inputFailure(at, "input field %q must hold 1 to %d bytes", field.Name, expected.MaxBytes)
		}
		switch expected.Format {
		case "JPEG":
			if _, format, err := image.DecodeConfig(bytes.NewReader(field.Data)); !bytes.HasPrefix(field.Data, []byte{0xFF, 0xD8, 0xFF}) || err != nil || format != "jpeg" {
				return inputFailure(at, "input field %q is not a JPEG image", field.Name)
			}
		case "UTF8":
			if !utf8.Valid(field.Data) {
				return inputFailure(at, "input field %q is not valid UTF-8", field.Name)
			}
		}
	}
	return nil
}

func inputFailure(field, format string, args ...any) error {
	return &Error{Code: "InvalidInput", Message: fmt.Sprintf(format, args...), Field: field}
}

func rawFieldNames(fields []RawField) []string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = field.Name
	}
	return names
}
