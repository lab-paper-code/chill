package profilingrun

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const (
	testActuatedNVPMode   = "MODE_15W_6CORE"
	testARM64Architecture = "arm64"
)

func TestCandidateReportMatchesMaterialSelectionSemantics(t *testing.T) {
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
			Image: "artifact@sha256:digest", Architecture: "amd64", Path: "/artifact/model.onnx",
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

func TestCandidateReportBindsSelectedActuatorTarget(t *testing.T) {
	intent := validIntent()
	intent.State.DeviceClass = "jetson-xavier-nx-8g"
	intent.Candidate.Inputs.DeviceClass.Name = intent.State.DeviceClass
	intent.State.PowerMode = testActuatedNVPMode
	intent.TargetNode.Architecture = testARM64Architecture
	intent.Actuation = &ActuationContract{
		PowerMode:  intent.State.PowerMode,
		PolicyName: "jetson-15w",
		Actuator: ActuatorTarget{
			Kind:           ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &JetsonNVPModelTarget{ID: 2},
		},
	}
	report := CandidateReport{
		SchemaVersion: CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs:        intent.Candidate.Inputs,
		Selection: CandidateReportSelection{
			ExecutionPath: intent.Candidate.ExecutionPath, Artifact: intent.State.Artifact,
			PowerMode: intent.State.PowerMode,
			Actuator:  &intent.Actuation.Actuator,
		},
		DeviceClassFacts: CandidateReportDeviceClassFacts{
			Architecture: intent.TargetNode.Architecture,
			NodeSelector: map[string]string{"edge.dacs.io/device-class": intent.State.DeviceClass},
			PowerModes:   []string{"MAXN", intent.State.PowerMode},
		},
		ArtifactSupply: CandidateReportArtifactSupply{
			Image: intent.Images.Artifact, Architecture: intent.TargetNode.Architecture,
			Path: intent.State.ArtifactPath,
		},
		MetricObserverImage: intent.Images.MetricObserver,
		Verdict:             intent.Candidate.Verdict,
		Reasons:             []CandidateReportReason{},
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCandidateReport(payload, intent); err != nil {
		t.Fatal(err)
	}

	drifted := report
	driftedTarget := *report.Selection.Actuator
	driftedNVPModel := *driftedTarget.JetsonNVPModel
	driftedNVPModel.ID = 3
	driftedTarget.JetsonNVPModel = &driftedNVPModel
	drifted.Selection.Actuator = &driftedTarget
	driftedPayload, err := json.Marshal(drifted)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCandidateReport(driftedPayload, intent); err == nil ||
		!strings.Contains(err.Error(), "actuator target") {
		t.Fatalf("candidate actuator drift was accepted: %v", err)
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

func TestCandidateReportValidatesHostContractDelivery(t *testing.T) {
	report := completeCandidateReportForTest()
	contract := rknnHostContractForTest()
	report.HostContract = &contract
	report.ArtifactCapability = rknnArtifactCapabilityForTest()
	report.RuntimeAccess = &CandidateReportRuntimeAccess{Privileged: true, SupplementalGroups: []int64{44, 110}}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	intent := Intent{
		Candidate: CandidateIdentity{
			Inputs: report.Inputs, ExecutionPath: report.Selection.ExecutionPath, Verdict: report.Verdict,
		},
		State: ExecutionState{
			DeviceClass: report.Inputs.DeviceClass.Name, Model: report.Inputs.ModelSpec.Name,
			Artifact:     report.Selection.Artifact,
			ArtifactPath: report.ArtifactSupply.Path, PowerMode: report.Selection.PowerMode,
			RuntimeFamily: "rknnlite", HostContract: cloneCandidateHostContract(report.HostContract),
		},
		Images: Images{
			Runtime: report.Inputs.RuntimeDeclaration.Image, Artifact: report.ArtifactSupply.Image,
			MetricObserver: report.MetricObserverImage,
		},
		TargetNode:  NodeSnapshot{Architecture: report.DeviceClassFacts.Architecture},
		Measurement: MeasurementContract{Batch: 1},
	}
	verified, err := VerifyCandidateReport(payload, intent)
	if err != nil {
		t.Fatal(err)
	}
	firstDelivery := verified.RuntimeDelivery()
	firstDelivery.HostContract.Devices[0].Path = "/mutated"
	firstDelivery.RuntimeAccess.SupplementalGroups[0] = 999
	secondDelivery := verified.RuntimeDelivery()
	if secondDelivery.HostContract.Devices[0].Path == "/mutated" ||
		secondDelivery.RuntimeAccess.SupplementalGroups[0] == 999 {
		t.Fatal("verified candidate report exposed mutable internal delivery state")
	}

	withoutPlatform := report
	withoutPlatformContract := *report.HostContract
	withoutPlatformContract.Platform = nil
	withoutPlatform.HostContract = &withoutPlatformContract
	withoutPlatformPayload, err := json.Marshal(withoutPlatform)
	if err != nil {
		t.Fatal(err)
	}
	withoutPlatformIntent := intent
	withoutPlatformIntent.State.HostContract = cloneCandidateHostContract(withoutPlatform.HostContract)
	if _, err := VerifyCandidateReport(withoutPlatformPayload, withoutPlatformIntent); err == nil ||
		!strings.Contains(err.Error(), "host platform") {
		t.Fatalf("RKNN candidate without host platform was accepted: %v", err)
	}

	intent.Measurement.Batch = 2
	if err := ValidateCandidateReport(payload, intent); err == nil {
		t.Fatal("expected fixed-batch artifact capability mismatch")
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
		TargetPlatform: "rk3588", FixedBatch: 1, InputShape: []int32{1, 224, 224, 3},
		InputLayout: RKNNLiteInputLayout, InputDataType: RKNNLiteInputDataType,
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
			Image: "artifact@sha256:digest", Architecture: testARM64Architecture, Path: "/artifact/model.rknn",
		},
		MetricObserverImage: "observer@sha256:digest",
		Verdict:             "Compatible", Reasons: []CandidateReportReason{},
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
			candidate := intent
			if err := ValidateCandidateReport(changed, candidate); err == nil {
				t.Fatal("expected candidate report identity rejection")
			}
		})
	}
}
