package serving

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

func TestValidateInputTensorBoundaries(t *testing.T) {
	for _, tc := range []struct {
		datatype, data string
		count          int32
		valid          bool
	}{
		{"INT8", "[-128,127]", 2, true}, {"INT8", "[-129]", 1, false},
		{"UINT8", "[0,255]", 2, true}, {"UINT8", "[256]", 1, false},
		{"INT16", "[-32768,32767]", 2, true}, {"INT16", "[-32769]", 1, false},
		{"UINT16", "[0,65535]", 2, true}, {"UINT16", "[65536]", 1, false},
		{"INT32", "[-2147483648,2147483647]", 2, true}, {"INT32", "[-2147483649]", 1, false},
		{"UINT32", "[0,4294967295]", 2, true}, {"UINT32", "[4294967296]", 1, false},
		{"INT64", "[-9223372036854775808,9223372036854775807]", 2, true},
		{"INT64", "[-9223372036854775809]", 1, false}, {"INT64", "[1.0]", 1, false},
		{"UINT64", "[0,18446744073709551615]", 2, true},
		{"UINT64", "[18446744073709551616]", 1, false}, {"UINT64", "[-0]", 1, false},
		{"FP16", "[-65504,65504]", 2, true}, {"FP16", "[65504.0001]", 1, false},
		{"FP32", "[-3.4028234663852886e38,3.4028234663852886e38]", 2, true},
		{"FP32", "[3.5e38]", 1, false}, {"FP32", "[ \n1e-3\t ]", 1, true},
		{"FP64", "[1.7976931348623157e308]", 1, true}, {"FP64", "[1e309]", 1, false},
		{"FP32", "[]", 0, true}, {"FP32", "[ ]", 0, true},
		{"FP32", "[]", 1, false}, {"FP32", "[1]", 0, false},
		{"FP32", "1", 1, false}, {"FP32", "null", 1, false}, {"FP32", "[null]", 1, false},
		{"FP32", "[\"1\"]", 1, false}, {"FP32", "[[1]]", 1, false}, {"FP32", "[{}]", 1, false},
		{"FP32", `["1,2"]`, 1, false}, {"FP32", "[[1,2]]", 2, false},
		{"FP32", "\u00a0[1]", 1, false}, {"FP32", "[1]\u00a0", 1, false},
		{"FP32", "[true]", 1, false}, {"FP32", "[1,]", 1, false}, {"FP32", "[01]", 1, false},
		{"FP32", "[+1]", 1, false}, {"FP32", "[NaN]", 1, false}, {"FP32", "[Infinity]", 1, false},
		{"FP32", "[1] []", 1, false}, {"FP32", "[1] false", 1, false}, {"FP32", "[1", 1, false},
		{"FP32", "[1 2]", 2, false}, {"FP32", "[1,2]", 1, false}, {"FP32", "[1]", 2, false},
		{"BOOL", "[true,false]", 2, true}, {"BOOL", "[1]", 1, false},
		{"BYTES", `["a,b","c\"d"]`, 2, true}, {"BYTES", "[null]", 1, false},
	} {
		t.Run(tc.datatype+":"+tc.data, func(t *testing.T) {
			shape := []int32{tc.count}
			input := &InferenceInput{Inputs: []TensorInput{{
				Name: "input", Datatype: tc.datatype, Shape: shape, Data: json.RawMessage(tc.data),
			}}}
			paths := []ModelPath{{Tensors: []modelcompat.InputTensor{{
				Name: "input", Datatype: tc.datatype, SampleShape: shape,
			}}}}
			_, err := validateInput(input, paths)
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected tensor boundary verdict: %v", err)
			}
		})
	}
}

func TestValidateInputKeepsDeclaredTensorContract(t *testing.T) {
	for _, mismatch := range []string{"name", "datatype", "shape", "duplicate"} {
		t.Run(mismatch, func(t *testing.T) {
			input := &InferenceInput{Inputs: []TensorInput{{
				Name: "input", Datatype: "FP32", Shape: []int32{1}, Data: json.RawMessage("[0]"),
			}}}
			paths := []ModelPath{{Tensors: []modelcompat.InputTensor{{
				Name: "input", Datatype: "FP32", SampleShape: []int32{1},
			}}}}
			switch mismatch {
			case "name":
				input.Inputs[0].Name = "other"
			case "datatype":
				input.Inputs[0].Datatype = "INT32"
			case "shape":
				input.Inputs[0].Shape = []int32{1, 1}
			case "duplicate":
				input.Inputs = append(input.Inputs, input.Inputs[0])
			}
			if _, err := validateInput(input, paths); err == nil {
				t.Fatal("mismatched declared tensor contract was admitted")
			}
		})
	}
}

func BenchmarkValidateInputImageFP32(b *testing.B) {
	const elements = 3 * 224 * 224
	data := json.RawMessage("[" + strings.Repeat("0.123456789,", elements-1) + "0.123456789]")
	shape := []int32{3, 224, 224}
	input := &InferenceInput{Inputs: []TensorInput{{Name: "input", Datatype: "FP32", Shape: shape, Data: data}}}
	paths := []ModelPath{{Tensors: []modelcompat.InputTensor{{Name: "input", Datatype: "FP32", SampleShape: shape}}}}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		if _, err := validateInput(input, paths); err != nil {
			b.Fatal(err)
		}
	}
}
