package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCandidateReportBindsEveryMaterialSelection(t *testing.T) {
	payload, err := os.ReadFile("testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	intentPayload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := decodeTestIntent(intentPayload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	intent.Candidate.ReportDigest = "sha256:" + hex.EncodeToString(sum[:])
	if err := ValidateCandidateReport(payload, intent); err != nil {
		t.Fatal(err)
	}
	intent.Candidate.ExecutionPath = "other"
	if err := ValidateCandidateReport(payload, intent); err == nil {
		t.Fatal("expected pre-run selection mismatch rejection")
	}
}

func TestDecodeInternalCandidateReportIsStrictAndComplete(t *testing.T) {
	report := CandidateReport{
		SchemaVersion: CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs: CandidateReportInputs{
			ModelSpec: CandidateReportObjectInput{
				Name: "model", UID: "model-uid", ResourceVersion: "1", ContentDigest: "sha256:model",
			},
			DeviceClass: CandidateReportObjectInput{
				Name: "class", UID: "class-uid", ResourceVersion: "2", ContentDigest: "sha256:class",
			},
			RuntimeDeclaration: CandidateReportRuntimeInput{
				Namespace: "chill-system", Name: "facts", UID: "facts-uid", ResourceVersion: "3",
				Key: "execution-facts.yaml", ContentDigest: "sha256:runtime", Image: "runtime@sha256:digest",
			},
		},
		Selection: CandidateReportSelection{
			ExecutionPath: "ort-cpu", Artifact: "model.onnx", ArtifactDigest: "sha256:artifact", PowerMode: "fixed",
		},
		DeviceClassFacts: CandidateReportDeviceClassFacts{
			Architecture: "amd64", NodeSelector: map[string]string{"class": "lattepanda"}, PowerModes: []string{"fixed"},
		},
		ArtifactSupply: CandidateReportArtifactSupply{
			Image: "artifact@sha256:digest", Architecture: "amd64", Path: "/artifact/model.onnx", Digest: "sha256:artifact",
		},
		MetricObserverImage: "observer@sha256:digest",
		Verdict:             "Compatible", Reasons: []CandidateReportReason{},
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err != nil {
		t.Fatal(err)
	}

	missing := report
	missing.ArtifactSupply.Path = ""
	payload, err = json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err == nil {
		t.Fatal("expected missing material fact rejection")
	}

	unknown := append(payload[:len(payload)-1], []byte(`,"unknown":true}`)...)
	if _, err := DecodeCandidateReport(unknown); err == nil {
		t.Fatal("expected unknown field rejection")
	}
	valid, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(append(valid, valid...)); err == nil {
		t.Fatal("expected multiple JSON values rejection")
	}
}

func TestCandidateReportReadsRetainedObserverImageName(t *testing.T) {
	payload := []byte(`{
  "schemaVersion":"chill.dacs.io/modelcompat-report.v1alpha1",
  "scope":"StaticCompatibility",
  "inputs":{},
  "selection":{},
  "powerObserverImage":"registry.example/observer@sha256:retained",
  "verdict":"Incompatible",
  "reasons":[]
}`)
	var report CandidateReport
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatal(err)
	}
	if report.MetricObserverImage != "registry.example/observer@sha256:retained" {
		t.Fatalf("observer image = %q", report.MetricObserverImage)
	}
	current, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), `"metricObserverImage"`) || strings.Contains(string(current), `"powerObserverImage"`) {
		t.Fatalf("current candidate report wire shape = %s", current)
	}
}

func TestCandidateReportRejectsUnsupportedSchemaAndCatalogNameDrift(t *testing.T) {
	payload, err := os.ReadFile("testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	intentPayload, err := os.ReadFile("testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := decodeTestIntent(intentPayload)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unsupported schema", mutate: func(report map[string]any) {
			report["schemaVersion"] = "foreign"
		}},
		{name: "ModelSpec name drift", mutate: func(report map[string]any) {
			report["inputs"].(map[string]any)["modelSpec"].(map[string]any)["name"] = "other-model"
		}},
		{name: "DeviceClass name drift", mutate: func(report map[string]any) {
			report["inputs"].(map[string]any)["deviceClass"].(map[string]any)["name"] = "other-class"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var report map[string]any
			if err := json.Unmarshal(payload, &report); err != nil {
				t.Fatal(err)
			}
			test.mutate(report)
			changed, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(changed)
			candidate := intent
			candidate.Candidate.ReportDigest = "sha256:" + hex.EncodeToString(sum[:])
			if err := ValidateCandidateReport(changed, candidate); err == nil {
				t.Fatal("expected candidate report identity rejection")
			}
		})
	}
}
