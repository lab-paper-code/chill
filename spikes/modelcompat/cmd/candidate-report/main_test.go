package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDigest = "sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2"

func TestRunDomainVerdicts(t *testing.T) {
	tests := []struct {
		name           string
		deviceArch     string
		verification   string
		wantVerdict    string
		wantReasonCode string
	}{
		{
			name:         "compatible",
			deviceArch:   "amd64",
			verification: trustedVerificationMethod,
			wantVerdict:  "Compatible",
		},
		{
			name:           "incompatible architecture",
			deviceArch:     "arm64",
			verification:   trustedVerificationMethod,
			wantVerdict:    "Incompatible",
			wantReasonCode: "RuntimeArchitectureMismatch",
		},
		{
			name:           "untrusted declaration",
			deviceArch:     "amd64",
			verification:   "unknown-producer",
			wantVerdict:    "Unknown",
			wantReasonCode: "RuntimeDeclarationUntrusted",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inputs := writeInputs(t, test.deviceArch, test.verification)
			var stdout, stderr bytes.Buffer
			code := run(inputs.args(), &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
			}
			var report compatibilityReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("decode report: %v; stdout=%q", err, stdout.String())
			}
			if report.Verdict != test.wantVerdict {
				t.Fatalf("verdict = %q, want %q", report.Verdict, test.wantVerdict)
			}
			if report.Scope != reportScope {
				t.Fatalf("scope = %q", report.Scope)
			}
			if test.wantReasonCode == "" {
				if len(report.Reasons) != 0 {
					t.Fatalf("reasons = %#v, want empty", report.Reasons)
				}
			} else if len(report.Reasons) != 1 || report.Reasons[0].Code != test.wantReasonCode {
				t.Fatalf("reasons = %#v, want %q", report.Reasons, test.wantReasonCode)
			}
			if !strings.Contains(stderr.String(), "CHILL STATIC COMPATIBILITY REPORT") ||
				!strings.Contains(stderr.String(), "not proven") {
				t.Fatalf("human boundary missing: %s", stderr.String())
			}
			if lines := strings.Count(strings.TrimSpace(stdout.String()), "\n"); lines != 0 {
				t.Fatalf("stdout must contain one JSON line: %q", stdout.String())
			}
		})
	}
}

func TestRunIsDeterministicAndPreservesExactInputIdentity(t *testing.T) {
	inputs := writeInputs(t, "amd64", trustedVerificationMethod)
	var firstOut, firstErr, secondOut, secondErr bytes.Buffer
	if code := run(inputs.args(), &firstOut, &firstErr); code != 0 {
		t.Fatalf("first exit = %d: %s", code, firstErr.String())
	}
	if code := run(inputs.args(), &secondOut, &secondErr); code != 0 {
		t.Fatalf("second exit = %d: %s", code, secondErr.String())
	}
	if firstOut.String() != secondOut.String() || firstErr.String() != secondErr.String() {
		t.Fatal("identical inputs produced different reports")
	}

	var before compatibilityReport
	if err := json.Unmarshal(firstOut.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputs.modelSpec, append(mustRead(t, inputs.modelSpec), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	var changedOut, changedErr bytes.Buffer
	if code := run(inputs.args(), &changedOut, &changedErr); code != 0 {
		t.Fatalf("changed exit = %d: %s", code, changedErr.String())
	}
	var after compatibilityReport
	if err := json.Unmarshal(changedOut.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if before.Inputs.ModelSpec.ContentDigest == after.Inputs.ModelSpec.ContentDigest {
		t.Fatal("ModelSpec content digest did not change with exact input bytes")
	}
	if before.Inputs.DeviceClass != after.Inputs.DeviceClass ||
		before.Inputs.RuntimeDeclaration != after.Inputs.RuntimeDeclaration {
		t.Fatal("unmodified input identities changed")
	}
}

func TestRunUsageAndProcessingFailures(t *testing.T) {
	inputs := writeInputs(t, "amd64", trustedVerificationMethod)
	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{name: "missing flags", args: nil, wantCode: 2},
		{name: "positional argument", args: append(inputs.args(), "extra"), wantCode: 2},
		{name: "unknown flag", args: []string{"-unknown"}, wantCode: 2},
		{name: "help", args: []string{"-help"}, wantCode: 0},
		{
			name: "missing file",
			args: []string{
				"-model-spec", "missing",
				"-device-class", inputs.deviceClass,
				"-runtime-declaration", inputs.runtimeDeclaration,
				"-execution-path", "ort-cpu",
			},
			wantCode: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != test.wantCode {
				t.Fatalf("exit code = %d, want %d; stderr=%s", code, test.wantCode, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("failure/help stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRunRejectsInvalidStaticInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testInputs)
	}{
		{
			name: "unknown ModelSpec field",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.modelSpec, "spec:\n", "spec:\n  compatibleClasses: []\n")
			},
		},
		{
			name: "duplicate ModelSpec key",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.modelSpec, "kind: ModelSpec\n", "kind: ModelSpec\nkind: DeviceClass\n")
			},
		},
		{
			name: "wrong ModelSpec GVK",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.modelSpec, "kind: ModelSpec", "kind: DeviceClass")
			},
		},
		{
			name: "second ModelSpec YAML document",
			mutate: func(inputs *testInputs) {
				appendFile(t, inputs.modelSpec, "---\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: hidden}\n")
			},
		},
		{
			name: "duplicate artifact name",
			mutate: func(inputs *testInputs) {
				appendFile(t, inputs.modelSpec, "")
				replace(
					t, inputs.modelSpec,
					"  executionPaths:\n",
					"    - name: canonical-onnx\n      format: onnx\n      digest: "+testDigest+"\n  executionPaths:\n",
				)
			},
		},
		{
			name: "malformed artifact digest",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.modelSpec, testDigest, "sha256:bad")
			},
		},
		{
			name: "dangling artifact reference",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.modelSpec, "artifact: canonical-onnx", "artifact: missing")
			},
		},
		{
			name: "missing execution path",
			mutate: func(inputs *testInputs) {
				inputs.executionPath = "missing"
			},
		},
		{
			name: "mutable runtime tag",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.runtimeDeclaration, "@sha256:"+strings.Repeat("a", 64), ":cpu-v1")
			},
		},
		{
			name: "wrong DeviceClass GVK",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.deviceClass, "kind: DeviceClass", "kind: ModelSpec")
			},
		},
		{
			name: "second DeviceClass YAML document",
			mutate: func(inputs *testInputs) {
				appendFile(t, inputs.deviceClass, "---\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: hidden}\n")
			},
		},
		{
			name: "duplicate DeviceClass key",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.deviceClass, "architecture: amd64", "architecture: amd64\n  architecture: arm64")
			},
		},
		{
			name: "unknown runtime field",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.runtimeDeclaration, `"architecture":"amd64"`, `"architecture":"amd64","node":"lattepanda"`)
			},
		},
		{
			name: "duplicate runtime key",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.runtimeDeclaration, `"architecture":"amd64"`, `"architecture":"amd64","architecture":"arm64"`)
			},
		},
		{
			name: "nested duplicate runtime key",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.runtimeDeclaration, `"family":"onnxruntime"`, `"family":"onnxruntime","family":"other"`)
			},
		},
		{
			name: "missing runtime architecture",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.runtimeDeclaration, `"architecture":"amd64"`, `"architecture":""`)
			},
		},
		{
			name: "missing runtime family",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.runtimeDeclaration, `"family":"onnxruntime"`, `"family":""`)
			},
		},
		{
			name: "missing verification",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.runtimeDeclaration, `"verification":"`+trustedVerificationMethod+`"`, `"verification":""`)
			},
		},
		{
			name: "omitted backend inventory",
			mutate: func(inputs *testInputs) {
				replace(t, inputs.runtimeDeclaration, `,"backends":["CPUExecutionProvider"]`, ``)
			},
		},
		{
			name: "trailing runtime object",
			mutate: func(inputs *testInputs) {
				appendFile(t, inputs.runtimeDeclaration, `{}`)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inputs := writeInputs(t, "amd64", trustedVerificationMethod)
			test.mutate(&inputs)
			var stdout, stderr bytes.Buffer
			if code := run(inputs.args(), &stdout, &stderr); code != 1 {
				t.Fatalf("exit code = %d, want 1; stderr=%s", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("processing failure emitted stdout: %q", stdout.String())
			}
		})
	}
}

type testInputs struct {
	modelSpec          string
	deviceClass        string
	runtimeDeclaration string
	executionPath      string
}

func (inputs testInputs) args() []string {
	return []string{
		"-model-spec", inputs.modelSpec,
		"-device-class", inputs.deviceClass,
		"-runtime-declaration", inputs.runtimeDeclaration,
		"-execution-path", inputs.executionPath,
	}
}

func writeInputs(t *testing.T, deviceArchitecture, verification string) testInputs {
	t.Helper()
	root := t.TempDir()
	inputs := testInputs{
		modelSpec:          filepath.Join(root, "model.yaml"),
		deviceClass:        filepath.Join(root, "device.yaml"),
		runtimeDeclaration: filepath.Join(root, "runtime.json"),
		executionPath:      "ort-cpu",
	}
	write(t, inputs.modelSpec, `apiVersion: edge.dacs.io/v1alpha1
kind: ModelSpec
metadata:
  name: mobilenet-v2-050
spec:
  artifacts:
    - name: canonical-onnx
      format: onnx
      digest: `+testDigest+`
  executionPaths:
    - name: ort-cpu
      artifact: canonical-onnx
      runtime:
        family: onnxruntime
        backend: CPUExecutionProvider
`)
	write(t, inputs.deviceClass, `apiVersion: edge.dacs.io/v1alpha1
kind: DeviceClass
metadata:
  name: lattepanda-3-delta-8g
spec:
  nodeSelector:
    edge.dacs.io/device-class: lattepanda-3-delta-8g
  architecture: `+deviceArchitecture+`
  memory: 8Gi
  accelerator: none
  powerModes:
    - name: fixed
`)
	backends := []string{"CPUExecutionProvider"}
	declaration := runtimeDeclaration{
		SchemaVersion: runtimeDeclarationSchema,
		Image:         "registry.example/chill/runtime@sha256:" + strings.Repeat("a", 64),
		Architecture:  "amd64",
		Verification:  verification,
	}
	declaration.Runtime.Family = "onnxruntime"
	declaration.Runtime.Backends = &backends
	payload, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	write(t, inputs.runtimeDeclaration, string(payload))
	return inputs
}

func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func replace(t *testing.T, path, old, new string) {
	t.Helper()
	value := string(mustRead(t, path))
	if !strings.Contains(value, old) {
		t.Fatalf("%q not found in %s", old, path)
	}
	write(t, path, strings.Replace(value, old, new, 1))
}

func appendFile(t *testing.T, path, suffix string) {
	t.Helper()
	write(t, path, string(mustRead(t, path))+suffix)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
