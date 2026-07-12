package modelcompat

import (
	"reflect"
	"testing"
)

const alternateArchitecture = "arm64"

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Input)
		wantVerdict Verdict
		wantReasons []Reason
	}{
		{name: "compatible", wantVerdict: VerdictCompatible},
		{
			name: "missing declaration",
			mutate: func(input *Input) {
				input.Runtime = nil
			},
			wantVerdict: VerdictUnknown,
			wantReasons: []Reason{unknown(ReasonRuntimeDeclarationMissing)},
		},
		{
			name: "missing declaration skips runtime dependent comparisons",
			mutate: func(input *Input) {
				input.DeviceClass.Architecture = ""
				input.Path.Backend = ""
				input.Runtime = nil
			},
			wantVerdict: VerdictUnknown,
			wantReasons: []Reason{unknown(ReasonRuntimeDeclarationMissing)},
		},
		{
			name: "untrusted declaration",
			mutate: func(input *Input) {
				input.Runtime.Trusted = false
				input.Runtime.Architecture = alternateArchitecture
				input.Runtime.Family = "other-runtime"
				input.Runtime.Backends = []string{}
			},
			wantVerdict: VerdictUnknown,
			wantReasons: []Reason{unknown(ReasonRuntimeDeclarationUntrusted)},
		},
		{
			name: "missing requirement facts",
			mutate: func(input *Input) {
				input.Artifact.Format = " "
				input.Path.RuntimeFamily = " "
				input.Path.Backend = " "
				input.DeviceClass.Architecture = " "
			},
			wantVerdict: VerdictUnknown,
			wantReasons: []Reason{
				unknown(ReasonArtifactFormatMissing),
				unknown(ReasonRequiredRuntimeFamilyMissing),
				unknown(ReasonDeviceArchitectureMissing),
				unknown(ReasonRequiredBackendMissing),
			},
		},
		{
			name: "unsupported artifact runtime relation",
			mutate: func(input *Input) {
				input.Artifact.Format = "future-format"
			},
			wantVerdict: VerdictUnknown,
			wantReasons: []Reason{unknown(ReasonArtifactRuntimeRelationUnknown)},
		},
		{
			name: "architecture mismatch",
			mutate: func(input *Input) {
				input.Runtime.Architecture = alternateArchitecture
			},
			wantVerdict: VerdictIncompatible,
			wantReasons: []Reason{conflict(ReasonRuntimeArchitectureMismatch)},
		},
		{
			name: "runtime family mismatch",
			mutate: func(input *Input) {
				input.Runtime.Family = "other-runtime"
				input.Runtime.Backends = nil
			},
			wantVerdict: VerdictIncompatible,
			wantReasons: []Reason{conflict(ReasonRuntimeFamilyMismatch)},
		},
		{
			name: "backend unavailable",
			mutate: func(input *Input) {
				input.Runtime.Backends = []string{"OtherBackend"}
			},
			wantVerdict: VerdictIncompatible,
			wantReasons: []Reason{conflict(ReasonRuntimeBackendUnavailable)},
		},
		{
			name: "explicit empty backend set",
			mutate: func(input *Input) {
				input.Runtime.Backends = []string{}
			},
			wantVerdict: VerdictIncompatible,
			wantReasons: []Reason{conflict(ReasonRuntimeBackendUnavailable)},
		},
		{
			name: "backend present among multiple values",
			mutate: func(input *Input) {
				input.Runtime.Backends = []string{"OtherBackend", "CPUExecutionProvider"}
			},
			wantVerdict: VerdictCompatible,
		},
		{
			name: "missing comparable runtime facts",
			mutate: func(input *Input) {
				input.Runtime.Architecture = ""
				input.Runtime.Family = ""
				input.Runtime.Backends = nil
			},
			wantVerdict: VerdictUnknown,
			wantReasons: []Reason{
				unknown(ReasonRuntimeArchitectureMissing),
				unknown(ReasonRuntimeFamilyMissing),
			},
		},
		{
			name: "missing backend inventory",
			mutate: func(input *Input) {
				input.Runtime.Backends = nil
			},
			wantVerdict: VerdictUnknown,
			wantReasons: []Reason{unknown(ReasonRuntimeBackendsMissing)},
		},
		{
			name: "conflict wins while unknown reasons remain",
			mutate: func(input *Input) {
				input.Artifact.Format = "future-format"
				input.Runtime.Architecture = alternateArchitecture
				input.Runtime.Backends = nil
			},
			wantVerdict: VerdictIncompatible,
			wantReasons: []Reason{
				unknown(ReasonArtifactRuntimeRelationUnknown),
				conflict(ReasonRuntimeArchitectureMismatch),
				unknown(ReasonRuntimeBackendsMissing),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := compatibleInput()
			if test.mutate != nil {
				test.mutate(&input)
			}
			got := Evaluate(input)
			if got.Verdict != test.wantVerdict {
				t.Fatalf("Verdict = %q, want %q", got.Verdict, test.wantVerdict)
			}
			if !reflect.DeepEqual(got.Reasons, test.wantReasons) {
				t.Fatalf("Reasons = %#v, want %#v", got.Reasons, test.wantReasons)
			}
		})
	}
}

func TestEvaluateIsDeterministicAndDoesNotMutateInput(t *testing.T) {
	input := compatibleInput()
	before := append([]string(nil), input.Runtime.Backends...)
	first := Evaluate(input)
	second := Evaluate(input)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated evaluation differs: first=%#v second=%#v", first, second)
	}
	if !reflect.DeepEqual(input.Runtime.Backends, before) {
		t.Fatalf("input backends mutated: got=%#v want=%#v", input.Runtime.Backends, before)
	}
}

func compatibleInput() Input {
	return Input{
		Artifact: ArtifactRequirement{Format: "onnx"},
		Path: ExecutionPathRequirement{
			RuntimeFamily: "onnxruntime",
			Backend:       "CPUExecutionProvider",
		},
		DeviceClass: DeviceClassInfo{Architecture: "amd64"},
		Runtime: &RuntimeDeclaration{
			Trusted:      true,
			Architecture: "amd64",
			Family:       "onnxruntime",
			Backends:     []string{"CPUExecutionProvider"},
		},
	}
}
