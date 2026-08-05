package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const (
	testActuatedNVPMode   = "MODE_15W_6CORE"
	testARM64Architecture = "arm64"
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
	intent.State.PowerMode = testActuatedNVPMode
	intent.TargetNode.Architecture = testARM64Architecture
	intent.Actuation = &ActuationContract{
		PowerMode:    intent.State.PowerMode,
		PolicyDigest: "sha256:" + strings.Repeat("b", 64),
		Actuator: ActuatorTarget{
			Kind:           ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &JetsonNVPModelTarget{ID: 2},
		},
	}
	report := CandidateReport{
		SchemaVersion: CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs: CandidateReportInputs{
			ModelSpec: CandidateReportObjectInput{
				Name: intent.State.Model, UID: "model-uid", ResourceVersion: "1",
				ContentDigest: intent.Candidate.ModelSpecContentDigest,
			},
			DeviceClass: CandidateReportObjectInput{
				Name: intent.State.DeviceClass, UID: "class-uid", ResourceVersion: "2",
				ContentDigest: intent.Candidate.DeviceClassContentDigest,
			},
			RuntimeDeclaration: CandidateReportRuntimeInput{
				Namespace: "chill-system", Name: "facts", UID: "facts-uid", ResourceVersion: "3",
				Key: "execution-facts.yaml", ContentDigest: intent.Candidate.RuntimeDeclarationDigest,
				Image: intent.Images.Runtime,
			},
		},
		Selection: CandidateReportSelection{
			ExecutionPath: intent.Candidate.ExecutionPath, Artifact: intent.State.Artifact,
			ArtifactDigest: intent.State.ArtifactDigest, PowerMode: intent.State.PowerMode,
			Actuator: &intent.Actuation.Actuator,
		},
		DeviceClassFacts: CandidateReportDeviceClassFacts{
			Architecture: intent.TargetNode.Architecture,
			NodeSelector: map[string]string{"edge.dacs.io/device-class": intent.State.DeviceClass},
			PowerModes:   []string{"MAXN", intent.State.PowerMode},
		},
		ArtifactSupply: CandidateReportArtifactSupply{
			Image: intent.Images.Artifact, Architecture: intent.TargetNode.Architecture,
			Path: intent.State.ArtifactPath, Digest: intent.State.ArtifactDigest,
		},
		MetricObserverImage: intent.Images.MetricObserver,
		Verdict:             intent.Candidate.Verdict,
		Reasons:             []CandidateReportReason{},
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	intent.Candidate.ReportDigest = "sha256:" + hex.EncodeToString(sum[:])
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
	driftedSum := sha256.Sum256(driftedPayload)
	intent.Candidate.ReportDigest = "sha256:" + hex.EncodeToString(driftedSum[:])
	if err := ValidateCandidateReport(driftedPayload, intent); err == nil ||
		!strings.Contains(err.Error(), "actuator target") {
		t.Fatalf("candidate actuator drift was accepted: %v", err)
	}
}

func TestDecodeCandidateReportRetainsV1Alpha5WithoutActuation(t *testing.T) {
	report := completeCandidateReportForTest()
	report.SchemaVersion = candidateReportSchemaVersionV1Alpha5
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err != nil {
		t.Fatal(err)
	}
	report.Selection.PowerMode = testActuatedNVPMode
	report.Selection.Actuator = &ActuatorTarget{
		Kind:           ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &JetsonNVPModelTarget{ID: 2},
	}
	payload, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err == nil || !strings.Contains(err.Error(), "retained") {
		t.Fatalf("retained report accepted actuation: %v", err)
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

func TestCandidateHostContractDigestIsOrderIndependentAndMaterial(t *testing.T) {
	contract := rknnHostContractForTest()
	contract.Libraries = append(contract.Libraries, CandidateReportHostLibrary{
		Name: "libother", HostPath: "/usr/lib/libother.so", ContainerPath: "/usr/lib/libother.so",
		Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	first, err := CandidateHostContractDigest(&contract)
	if err != nil {
		t.Fatal(err)
	}
	reordered := contract
	reordered.Libraries = []CandidateReportHostLibrary{contract.Libraries[1], contract.Libraries[0]}
	reordered.Devices = append([]CandidateReportHostDevice(nil), contract.Devices...)
	for left, right := 0, len(reordered.Devices)-1; left < right; left, right = left+1, right-1 {
		reordered.Devices[left], reordered.Devices[right] = reordered.Devices[right], reordered.Devices[left]
	}
	second, err := CandidateHostContractDigest(&reordered)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent host contract order changed digest: %s != %s", first, second)
	}

	changed := contract
	changed.Driver.Version = "0.9.7"
	third, err := CandidateHostContractDigest(&changed)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("driver version did not change host contract digest")
	}
	if none, err := CandidateHostContractDigest(nil); err != nil || none != "" {
		t.Fatalf("nil host contract digest = %q, %v", none, err)
	}
}

func TestCandidateReportBindsHostContractDigest(t *testing.T) {
	report := completeCandidateReportForTest()
	contract := rknnHostContractForTest()
	report.HostContract = &contract
	report.ArtifactCapability = rknnArtifactCapabilityForTest()
	report.RuntimeAccess = &CandidateReportRuntimeAccess{Privileged: true, SupplementalGroups: []int64{44, 110}}
	hostDigest, err := CandidateHostContractDigest(report.HostContract)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	reportSum := sha256.Sum256(payload)
	intent := Intent{
		Candidate: CandidateIdentity{
			ReportDigest:             "sha256:" + hex.EncodeToString(reportSum[:]),
			ModelSpecContentDigest:   report.Inputs.ModelSpec.ContentDigest,
			DeviceClassContentDigest: report.Inputs.DeviceClass.ContentDigest,
			RuntimeDeclarationDigest: report.Inputs.RuntimeDeclaration.ContentDigest,
			ExecutionPath:            report.Selection.ExecutionPath, Verdict: report.Verdict,
		},
		State: ExecutionState{
			DeviceClass: report.Inputs.DeviceClass.Name, Model: report.Inputs.ModelSpec.Name,
			Artifact: report.Selection.Artifact, ArtifactDigest: report.Selection.ArtifactDigest,
			ArtifactPath: report.ArtifactSupply.Path, PowerMode: report.Selection.PowerMode,
			RuntimeFamily: "rknnlite", HostContractDigest: hostDigest,
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
	withoutPlatformSum := sha256.Sum256(withoutPlatformPayload)
	withoutPlatformIntent := intent
	withoutPlatformIntent.Candidate.ReportDigest = "sha256:" + hex.EncodeToString(withoutPlatformSum[:])
	withoutPlatformIntent.State.HostContractDigest, err = CandidateHostContractDigest(withoutPlatform.HostContract)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCandidateReport(withoutPlatformPayload, withoutPlatformIntent); err == nil ||
		!strings.Contains(err.Error(), "host platform") {
		t.Fatalf("RKNN candidate without host platform was accepted: %v", err)
	}

	intent.State.HostContractDigest = "sha256:" + strings.Repeat("f", 64)
	if err := ValidateCandidateReport(payload, intent); err == nil {
		t.Fatal("expected candidate host contract digest mismatch")
	}
	intent.State.HostContractDigest = hostDigest
	intent.Measurement.Batch = 2
	if err := ValidateCandidateReport(payload, intent); err == nil {
		t.Fatal("expected fixed-batch artifact capability mismatch")
	}
}

func rknnHostContractForTest() CandidateReportHostContract {
	return CandidateReportHostContract{
		Libraries: []CandidateReportHostLibrary{{
			Name: "librknnrt", HostPath: "/usr/lib/librknnrt.so", ContainerPath: "/usr/lib/librknnrt.so",
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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

func TestDecodeCandidateReportRetainsV1Alpha2EvidenceCompatibility(t *testing.T) {
	payload, err := os.ReadFile("testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := DecodeCandidateReport(payload)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != candidateReportSchemaVersionV1Alpha2 {
		t.Fatalf("decoded schema = %q", report.SchemaVersion)
	}

	invalid := completeCandidateReportForTest()
	invalid.SchemaVersion = candidateReportSchemaVersionV1Alpha2
	invalid.RuntimePackages = []CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}}
	payload, err = json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err == nil {
		t.Fatal("expected v1alpha2 report with v1alpha3 fields rejection")
	}
}

func TestDecodeCandidateReportRetainsV1Alpha3EvidenceCompatibility(t *testing.T) {
	report := completeCandidateReportForTest()
	report.SchemaVersion = candidateReportSchemaVersionV1Alpha3
	report.RuntimePackages = []CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}}
	contract := rknnHostContractForTest()
	contract.Platform = nil
	report.HostContract = &contract
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err != nil {
		t.Fatal(err)
	}

	contract.Platform = &CandidateReportHostPlatform{
		CompatiblePath: "/proc/device-tree/compatible", CompatibleToken: "rockchip,rk3588",
	}
	report.HostContract = &contract
	payload, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err == nil {
		t.Fatal("expected v1alpha3 report with v1alpha4 host platform rejection")
	}
}

func TestDecodeCandidateReportRetainsV1Alpha4EvidenceCompatibility(t *testing.T) {
	report := completeCandidateReportForTest()
	report.SchemaVersion = candidateReportSchemaVersionV1Alpha4
	report.RuntimePackages = []CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}}
	contract := rknnHostContractForTest()
	report.HostContract = &contract
	report.ArtifactCapability = &CandidateArtifactCapability{TargetPlatform: "rk3588", FixedBatch: 1}
	report.RuntimeAccess = &CandidateReportRuntimeAccess{Privileged: true, SupplementalGroups: []int64{44, 110}}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err != nil {
		t.Fatal(err)
	}
	report.ArtifactCapability.InputShape = []int32{1, 224, 224, 3}
	report.ArtifactCapability.InputLayout = RKNNLiteInputLayout
	report.ArtifactCapability.InputDataType = RKNNLiteInputDataType
	payload, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidateReport(payload); err == nil {
		t.Fatal("v1alpha4 candidate report accepted a v1alpha5 artifact input contract")
	}
}

func completeCandidateReportForTest() CandidateReport {
	return CandidateReport{
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
			ExecutionPath: "rknnlite", Artifact: "model.rknn", ArtifactDigest: "sha256:artifact", PowerMode: "fixed",
		},
		DeviceClassFacts: CandidateReportDeviceClassFacts{
			Architecture: testARM64Architecture, NodeSelector: map[string]string{"class": "orangepi"}, PowerModes: []string{"fixed"},
		},
		ArtifactSupply: CandidateReportArtifactSupply{
			Image: "artifact@sha256:digest", Architecture: testARM64Architecture, Path: "/artifact/model.rknn", Digest: "sha256:artifact",
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
			sum := sha256.Sum256(changed)
			candidate := intent
			candidate.Candidate.ReportDigest = "sha256:" + hex.EncodeToString(sum[:])
			if err := ValidateCandidateReport(changed, candidate); err == nil {
				t.Fatal("expected candidate report identity rejection")
			}
		})
	}
}
