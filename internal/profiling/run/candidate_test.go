package profilingrun

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

const (
	testActuatedNVPMode   = "MODE_15W_6CORE"
	testARM64Architecture = "arm64"
	testDifferentIdentity = "other"
	testMutatedValue      = "mutated"
	testMutatedPath       = "/mutated"
	testINT64Datatype     = "INT64"
	testDatatypeField     = "datatype"
	testShapeField        = "shape"
)

func TestDecodeInternalCandidateReportRequiresSelectedMaterial(t *testing.T) {
	report := CandidateReport{
		SchemaVersion: CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs: CandidateReportInputs{
			ModelSpec: CandidateReportObjectInput{
				Name: "model", UID: "model-uid", ResourceVersion: "1",
			},
			DeviceClass: CandidateReportObjectInput{
				Name: "class", UID: "class-uid", ResourceVersion: "2",
			},
			RuntimeDeclaration: CandidateReportRuntimeInput{
				Namespace: "chill-system", Name: "facts", UID: "facts-uid", ResourceVersion: "3",
				Key: "execution-facts.yaml", Image: "runtime@sha256:digest",
			},
		},
		Selection: CandidateReportSelection{
			ExecutionPath: "ort-cpu", Artifact: "model.onnx", PowerMode: "fixed",
		},
		DeviceClassFacts: CandidateReportDeviceClassFacts{
			Architecture: "amd64", NodeSelector: map[string]string{"class": "lattepanda"}, PowerModes: []string{"fixed"},
		},
		ArtifactSupply: CandidateReportArtifactSupply{
			Image: "artifact@sha256:digest", Architecture: "amd64", Files: []string{"/artifact/model.onnx"},
		},
		ArtifactCapability:  &CandidateArtifactCapability{},
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
	missing.ArtifactSupply.Files = nil
	payload, err = json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err == nil {
		t.Fatal("expected missing material fact rejection")
	}

	valid, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(append(valid, valid...)); err == nil {
		t.Fatal("expected multiple JSON values rejection")
	}
	var explicitNull map[string]any
	if err := json.Unmarshal(valid, &explicitNull); err != nil {
		t.Fatal(err)
	}
	explicitNull["selection"].(map[string]any)["actuator"] = nil
	nullPayload, err := json.Marshal(explicitNull)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(nullPayload); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("explicit null candidate actuator was accepted: %v", err)
	}
}

func TestDecodeCandidateReportValidatesSeparatedHostContract(t *testing.T) {
	report := completeCandidateReportForTest()
	report.RuntimePackages = []CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}}
	contract := rknnHostContractForTest()
	report.HostContract = &contract
	report.ArtifactCapability = rknnArtifactCapabilityForTest()
	report.RuntimeAccess = &CandidateReportRuntimeAccess{Privileged: true, SupplementalGroups: []int64{44, 110}}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err != nil {
		t.Fatal(err)
	}

	invalid := report
	invalidContract := *report.HostContract
	invalidContract.Allocation.ExclusivityClaimed = true
	invalid.HostContract = &invalidContract
	payload, err = json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err == nil {
		t.Fatal("expected exclusivity claim without Kubernetes resource rejection")
	}
}

func rknnHostContractForTest() CandidateReportHostContract {
	return CandidateReportHostContract{
		Libraries: []CandidateReportHostLibrary{{
			Name: "librknnrt", HostPath: "/usr/lib/librknnrt.so", ContainerPath: "/usr/lib/librknnrt.so",
		}},
		Driver: CandidateReportHostDriver{Name: "rknpu", VersionPath: "/sys/module/rknpu/version", Version: "0.9.6"},
		Devices: []CandidateReportHostDevice{
			{Path: "/dev/dri/card0"},
			{Path: "/dev/dri/card1"},
			{Path: "/dev/dri/renderD128"},
			{Path: "/dev/dri/renderD129"},
		},
		Platform: &CandidateReportHostPlatform{
			CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588",
		},
	}
}

func rknnArtifactCapabilityForTest() *CandidateArtifactCapability {
	return &CandidateArtifactCapability{
		TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{1},
		Tensors: []modelcompat.InputTensor{{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}}},
	}
}

func completeCandidateReportForTest() CandidateReport {
	return CandidateReport{
		SchemaVersion: CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs: CandidateReportInputs{
			ModelSpec: CandidateReportObjectInput{
				Name: "model", UID: "model-uid", ResourceVersion: "1",
			},
			DeviceClass: CandidateReportObjectInput{
				Name: "class", UID: "class-uid", ResourceVersion: "2",
			},
			RuntimeDeclaration: CandidateReportRuntimeInput{
				Namespace: "chill-system", Name: "facts", UID: "facts-uid", ResourceVersion: "3",
				Key: "execution-facts.yaml", Image: "runtime@sha256:digest",
			},
		},
		Selection: CandidateReportSelection{
			ExecutionPath: "rknnlite", Artifact: "model.rknn", PowerMode: "fixed",
		},
		DeviceClassFacts: CandidateReportDeviceClassFacts{
			Architecture: testARM64Architecture, NodeSelector: map[string]string{"class": "orangepi"}, PowerModes: []string{"fixed"},
		},
		ArtifactSupply: CandidateReportArtifactSupply{
			Image: "artifact@sha256:digest", Architecture: testARM64Architecture, Files: []string{"/artifact/model.rknn"},
		},
		ArtifactCapability:  rknnArtifactCapabilityForTest(),
		MetricObserverImage: "observer@sha256:digest",
		Verdict:             "Compatible", Reasons: []CandidateReportReason{},
	}
}
