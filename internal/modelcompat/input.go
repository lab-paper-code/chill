package modelcompat

import (
	"errors"
	"fmt"
	"strings"
)

// InputTensor is one named, batch-invariant Triton input declaration.
// Frozen profiling records preserve this contract without runtime inference.
type InputTensor struct {
	Name        string  `json:"name"`
	Datatype    string  `json:"datatype"`
	SampleShape []int32 `json:"sampleShape"`
}

// ValidateInputTensors validates declarations, not a worker's support policy.
func ValidateInputTensors(tensors []InputTensor) error {
	if len(tensors) == 0 {
		return errors.New("input tensors are required")
	}
	seen := make(map[string]struct{}, len(tensors))
	for _, tensor := range tensors {
		if tensor.Name == "" || strings.TrimSpace(tensor.Name) != tensor.Name {
			return errors.New("input tensor name is required without surrounding whitespace")
		}
		if _, exists := seen[tensor.Name]; exists {
			return fmt.Errorf("duplicate input tensor name %q", tensor.Name)
		}
		seen[tensor.Name] = struct{}{}
		switch tensor.Datatype {
		case "BOOL", "UINT8", "UINT16", "UINT32", "UINT64", "INT8", "INT16", "INT32", "INT64", "FP16", "FP32", "FP64", "BYTES", "BF16":
		default:
			return fmt.Errorf("input tensor %q has invalid Triton datatype %q", tensor.Name, tensor.Datatype)
		}
		if len(tensor.SampleShape) == 0 {
			return fmt.Errorf("input tensor %q sampleShape is required", tensor.Name)
		}
		for _, dimension := range tensor.SampleShape {
			if dimension < 1 {
				return fmt.Errorf("input tensor %q sampleShape dimensions must be positive", tensor.Name)
			}
		}
	}
	return nil
}

// CloneInputTensors protects frozen identities from caller-owned slices.
func CloneInputTensors(tensors []InputTensor) []InputTensor {
	if tensors == nil {
		return nil
	}
	result := make([]InputTensor, len(tensors))
	for index, tensor := range tensors {
		result[index] = tensor
		result[index].SampleShape = append([]int32(nil), tensor.SampleShape...)
	}
	return result
}
