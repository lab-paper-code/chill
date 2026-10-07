package serving

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"strconv"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

// InferenceInput is one sample. The batch axis is added only by the backend
// adapter; external callers cannot override the registered model or SLO.
type InferenceInput struct {
	Inputs []TensorInput `json:"inputs"`
}

type TensorInput struct {
	Name     string          `json:"name"`
	Datatype string          `json:"datatype"`
	Shape    []int32         `json:"shape"`
	Data     json.RawMessage `json:"data"`
}

type validatedInput struct {
	input *InferenceInput
	path  int
}

func validateInput(input *InferenceInput, paths []ModelPath) (validatedInput, error) {
	if input == nil || len(input.Inputs) == 0 {
		return validatedInput{}, failure("InvalidInput", "inputs are required")
	}
	var contract []modelcompat.InputTensor
	pathIndex := 0
	for i := range paths {
		if inputMatches(input, paths[i].Tensors) {
			contract = paths[i].Tensors
			pathIndex = i
			break
		}
	}
	if contract == nil {
		return validatedInput{}, failure("InvalidInput", "input names, datatypes and sample shapes do not match a declared model input contract")
	}
	for _, tensor := range input.Inputs {
		count := 1
		for _, dimension := range tensor.Shape {
			if int64(dimension) > int64(math.MaxInt/count) {
				return validatedInput{}, failure("InvalidInput", "input tensor %q is too large", tensor.Name)
			}
			count *= int(dimension)
		}
		if err := validateTensorData(tensor, count); err != nil {
			return validatedInput{}, err
		}
	}
	return validatedInput{input: input, path: pathIndex}, nil
}

func inputMatches(input *InferenceInput, contract []modelcompat.InputTensor) bool {
	if len(input.Inputs) != len(contract) {
		return false
	}
	// ponytail: tensor lists are small; an index only earns its place if measured
	// request parsing shows this scan, rather than inference, is the bottleneck.
	for _, expected := range contract {
		matches := 0
		for _, actual := range input.Inputs {
			if actual.Name == expected.Name {
				if actual.Datatype != expected.Datatype || !slices.Equal(actual.Shape, expected.SampleShape) {
					return false
				}
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	return true
}

func validateTensorData(input TensorInput, expected int) error {
	decoder := json.NewDecoder(bytes.NewReader(input.Data))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('[') {
		return failure("InvalidInput", "input tensor %q data must be a flat JSON array", input.Name)
	}
	count := 0
	for decoder.More() {
		value, err := decoder.Token()
		if err != nil {
			return failure("InvalidInput", "input tensor %q contains invalid JSON data", input.Name)
		}
		if !validDatum(input.Datatype, value) {
			return failure("InvalidInput", "input tensor %q data is outside the supported %s value range", input.Name, input.Datatype)
		}
		count++
		if count > expected {
			return failure("InvalidInput", "input tensor %q data count does not match its sample shape", input.Name)
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') || count != expected {
		return failure("InvalidInput", "input tensor %q data count does not match its sample shape", input.Name)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return failure("InvalidInput", "input tensor %q data contains a trailing JSON value", input.Name)
	}
	return nil
}

func validDatum(datatype string, value any) bool {
	if datatype == "BOOL" {
		_, ok := value.(bool)
		return ok
	}
	if datatype == "BYTES" {
		_, ok := value.(string)
		return ok
	}
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	var bits int
	switch datatype {
	case "INT8", "UINT8":
		bits = 8
	case "INT16", "UINT16":
		bits = 16
	case "INT32", "UINT32":
		bits = 32
	case "INT64", "UINT64":
		bits = 64
	case "FP16", "FP32", "FP64":
		bits = 64
		if datatype == "FP32" {
			bits = 32
		}
		parsed, err := strconv.ParseFloat(string(number), bits)
		return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && (datatype != "FP16" || math.Abs(parsed) <= 65504)
	default:
		// Triton HTTP JSON has no BF16 numeric representation. A binary codec
		// must be implemented before that declared datatype can be served.
		return false
	}
	if datatype[0] == 'U' {
		_, err := strconv.ParseUint(string(number), 10, bits)
		return err == nil
	}
	_, err := strconv.ParseInt(string(number), 10, bits)
	return err == nil
}
