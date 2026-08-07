package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testArtifactPath        = "/artifact/mobilenet-v2-050.onnx"
	testArchitecture        = "amd64"
	testARM64Architecture   = "arm64"
	testSelectablePowerMode = "15W"
	testRuntimeImage        = "registry.example/chill/runtime@sha256:1aa023ab6fb43fd8f8a978036878849b7a5a0a2f0621e510b534fc767ea5774b"
	testArtifactImage       = "registry.example/chill/artifact@sha256:8cf7dea5f1b9cb00fcb1511c0f63c187e3d1cceaa44038e1c705d61f0714b2aa"
	testMetricObserverImage = "registry.example/chill/metric-observer@sha256:522325aaeb2ce2e2fea91372ebd8d931c1507c73687e4d20d70add6714c5077c"
)

func TestKubernetesCandidateResolverBuildsBoundCompatibleCandidate(t *testing.T) {
	resolver := newCandidateResolver(t, validCandidateObjects()...)
	selection := validCandidateSelection()

	resolved, err := resolver.ResolveCandidate(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	assertResolvedCandidate(t, resolved, selection)
	assertCandidateReport(t, resolved, selection)
}

func TestKubernetesCandidateResolverSeparatesRKNNRuntimeAndHostFacts(t *testing.T) {
	objects := validRKNNCandidateObjects()
	resolver := newCandidateResolver(t, objects...)
	selection := profiler.CandidateSelection{
		ModelSpec: "mobilenet-v2-050", DeviceClass: "orangepi-5-plus-16g",
		ExecutionPath: "rknnlite", PowerMode: fixedPowerMode,
	}

	resolved, err := resolver.ResolveCandidate(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	report, err := profilingrun.DecodeCandidateReport(resolved.ReportBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.RuntimePackages) != 1 || report.RuntimePackages[0].Name != "rknn-toolkit-lite2" ||
		report.RuntimePackages[0].Version != "2.3.2" {
		t.Fatalf("runtime packages were not retained separately: %#v", report.RuntimePackages)
	}
	if report.HostContract == nil || len(report.HostContract.Libraries) != 1 ||
		report.HostContract.Driver.Version != "0.9.6" || len(report.HostContract.Devices) != 4 ||
		report.HostContract.Platform == nil || report.HostContract.Platform.CompatibleToken != "rockchip,rk3588" ||
		report.HostContract.Allocation.ResourceName != "" || report.HostContract.Allocation.ExclusivityClaimed {
		t.Fatalf("host facts or allocation claim were conflated: %#v", report.HostContract)
	}
	if report.ArtifactCapability == nil || report.ArtifactCapability.TargetPlatform != "rk3588" ||
		report.ArtifactCapability.FixedBatch != 1 ||
		!slices.Equal(report.ArtifactCapability.InputShape, []int32{1, 224, 224, 3}) ||
		report.ArtifactCapability.InputLayout != profilingrun.RKNNLiteInputLayout ||
		report.ArtifactCapability.InputDataType != profilingrun.RKNNLiteInputDataType || report.RuntimeAccess == nil ||
		!report.RuntimeAccess.Privileged || !slices.Equal(report.RuntimeAccess.SupplementalGroups, []int64{44, 110}) {
		t.Fatalf("artifact capability or runtime access was not retained: %#v %#v",
			report.ArtifactCapability, report.RuntimeAccess)
	}
	if !reflect.DeepEqual(resolved.State.HostContract, report.HostContract) {
		t.Fatalf("frozen host contract = %#v, want %#v", resolved.State.HostContract, report.HostContract)
	}
}

func TestKubernetesCandidateResolverRejectsRKNNArtifactWithoutInputContract(t *testing.T) {
	objects := validRKNNCandidateObjects()
	model := objects[0].(*edgev1alpha1.ModelSpec)
	model.Spec.Artifacts[1].Derivation.Input = nil
	resolver := newCandidateResolver(t, objects...)
	_, err := resolver.ResolveCandidate(context.Background(), profiler.CandidateSelection{
		ModelSpec: "mobilenet-v2-050", DeviceClass: "orangepi-5-plus-16g",
		ExecutionPath: profilingrun.RKNNLiteExecutionPath, PowerMode: fixedPowerMode,
	})
	if err == nil || !strings.Contains(err.Error(), "input tensor contract") {
		t.Fatalf("RKNN artifact without input tensor contract was accepted: %v", err)
	}
}

func TestDecodeRuntimeDeclarationValidatesImageOwnedPackages(t *testing.T) {
	payload := validRKNNRuntimeDeclaration()
	declaration, err := decodeRuntimeDeclaration(runtimeDeclarationSource{
		Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(declaration.Packages) != 1 || declaration.Packages[0].Name != "rknn-toolkit-lite2" {
		t.Fatalf("decoded packages = %#v", declaration.Packages)
	}

	duplicate := strings.Replace(
		payload,
		`"packages":[{"name":"rknn-toolkit-lite2","version":"2.3.2"}]`,
		`"packages":[{"name":"rknn-toolkit-lite2","version":"2.3.2"},{"name":"rknn-toolkit-lite2","version":"2.3.2"}]`,
		1,
	)
	if _, err := decodeRuntimeDeclaration(runtimeDeclarationSource{
		Payload: duplicate,
	}); err == nil || !strings.Contains(err.Error(), "duplicate runtime package") {
		t.Fatalf("expected duplicate package rejection, got %v", err)
	}
}

func TestValidateHostExecutionContractFailsClosed(t *testing.T) {
	valid := hostExecutionContract{
		Libraries: []hostLibraryContract{{
			Name: "librknnrt", HostPath: "/usr/lib/librknnrt.so", ContainerPath: "/usr/lib/librknnrt.so",
		}},
		Driver:  hostDriverContract{Name: "rknpu", VersionPath: "/sys/module/rknpu/version", Version: "0.9.6"},
		Devices: []hostDeviceContract{{Path: "/dev/dri/renderD128"}},
	}
	if err := validateHostExecutionContract(valid); err != nil {
		t.Fatal(err)
	}

	invalidPath := valid
	invalidPath.Devices = []hostDeviceContract{{Path: "/dev/dri/../dri/renderD128"}}
	if err := validateHostExecutionContract(invalidPath); err == nil {
		t.Fatal("expected non-canonical host device path rejection")
	}

	invalidAllocation := valid
	invalidAllocation.Allocation.ExclusivityClaimed = true
	if err := validateHostExecutionContract(invalidAllocation); err == nil {
		t.Fatal("expected exclusivity claim without Kubernetes resource rejection")
	}

	invalidAllocation = valid
	invalidAllocation.Allocation.ResourceName = "npu"
	if err := validateHostExecutionContract(invalidAllocation); err == nil {
		t.Fatal("expected non-qualified extended-resource name rejection")
	}
}

func TestValidateRuntimeAccessContractFailsClosed(t *testing.T) {
	valid := runtimeAccessContract{Privileged: true, SupplementalGroups: []int64{44, 110}}
	if err := validateRuntimeAccessContract(valid); err != nil {
		t.Fatal(err)
	}
	duplicate := valid
	duplicate.SupplementalGroups = []int64{110, 110}
	if err := validateRuntimeAccessContract(duplicate); err == nil {
		t.Fatal("expected duplicate supplemental group rejection")
	}
	invalid := valid
	invalid.SupplementalGroups = []int64{-1}
	if err := validateRuntimeAccessContract(invalid); err == nil {
		t.Fatal("expected negative supplemental group rejection")
	}
}

func assertResolvedCandidate(t *testing.T, resolved profiler.ResolvedCandidate, selection profiler.CandidateSelection) {
	t.Helper()
	if resolved.Verdict != "Compatible" || resolved.State.PowerMode != fixedPowerMode ||
		resolved.State.HostContract != nil ||
		resolved.State.ArtifactPath != testArtifactPath ||
		resolved.RuntimeImage != testRuntimeImage || resolved.ArtifactImage != testArtifactImage ||
		resolved.ArtifactArchitecture != testArchitecture || resolved.ArtifactPath != testArtifactPath ||
		resolved.MetricObserverImage != testMetricObserverImage ||
		resolved.DeviceArchitecture != testArchitecture ||
		resolved.DeviceNodeSelector["edge.dacs.io/device-class"] != selection.DeviceClass {
		t.Fatalf("unexpected resolved candidate: %#v", resolved)
	}
	if len(resolved.DevicePowerModes) != 1 || resolved.DevicePowerModes[0] != fixedPowerMode {
		t.Fatalf("unexpected DeviceClass power modes: %#v", resolved.DevicePowerModes)
	}
}

func assertCandidateReport(t *testing.T, resolved profiler.ResolvedCandidate, selection profiler.CandidateSelection) {
	t.Helper()
	intent := profilingrun.Intent{
		Candidate: profilingrun.CandidateIdentity{
			Inputs: resolved.Inputs, ExecutionPath: selection.ExecutionPath, Verdict: resolved.Verdict,
		},
		State: resolved.State,
		Images: profilingrun.Images{
			Runtime: resolved.RuntimeImage, Artifact: resolved.ArtifactImage,
			MetricObserver: resolved.MetricObserverImage,
		},
		TargetNode: profilingrun.NodeSnapshot{Architecture: resolved.DeviceArchitecture},
	}
	if err := profilingrun.ValidateCandidateReport(resolved.ReportBytes, intent); err != nil {
		t.Fatalf("generated report is not consumable by frozen intent: %v", err)
	}

	report, err := profilingrun.DecodeCandidateReport(resolved.ReportBytes)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != profilingrun.CandidateReportSchemaVersion || report.Inputs.ModelSpec.UID != "model-uid" ||
		report.Inputs.ModelSpec.ResourceVersion != "10" ||
		report.Inputs.RuntimeDeclaration.Namespace != testSystemNamespace ||
		report.Inputs.RuntimeDeclaration.Name != "chill-profiler-facts" ||
		report.Inputs.RuntimeDeclaration.UID != "config-uid" ||
		report.Inputs.RuntimeDeclaration.ResourceVersion != "30" ||
		report.Inputs.RuntimeDeclaration.Key != "execution-facts.yaml" ||
		report.Selection.PowerMode != fixedPowerMode ||
		report.DeviceClassFacts.Architecture != testArchitecture ||
		report.DeviceClassFacts.NodeSelector["edge.dacs.io/device-class"] != selection.DeviceClass ||
		report.ArtifactSupply.Image != testArtifactImage ||
		report.ArtifactSupply.Path != testArtifactPath {
		t.Fatalf("candidate report lost source identity: %#v", report)
	}
}

func TestKubernetesCandidateResolverPreservesSnapshotSemantics(t *testing.T) {
	objects := validCandidateObjects()
	resolver := newCandidateResolver(t, objects...)
	first, err := resolver.ResolveCandidate(context.Background(), validCandidateSelection())
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolver.ResolveCandidate(context.Background(), validCandidateSelection())
	if err != nil {
		t.Fatal(err)
	}
	var firstReport, secondReport profilingrun.CandidateReport
	if json.Unmarshal(first.ReportBytes, &firstReport) != nil || json.Unmarshal(second.ReportBytes, &secondReport) != nil ||
		!reflect.DeepEqual(firstReport, secondReport) {
		t.Fatal("identical Kubernetes snapshots produced different candidate semantics")
	}
}

func TestKubernetesCandidateResolverFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]client.Object)
		match  string
	}{
		{
			name: "power mode absent from DeviceClass",
			mutate: func(objects []client.Object) {
				objects[1].(*edgev1alpha1.DeviceClass).Spec.PowerModes[0].Name = testSelectablePowerMode
			},
			match: `power mode "fixed" is not declared`,
		},
		{
			name: "fixed mode has actuator identity",
			mutate: func(objects []client.Object) {
				objects[1].(*edgev1alpha1.DeviceClass).Spec.PowerModes[0].Actuator = edgev1alpha1.PowerModeActuator{
					Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
					JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 0},
				}
			},
			match: "fixed mode name must use the Fixed actuator",
		},
		{
			name: "runtime architecture incompatible",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				replaceRuntimeDeclaration(config, `"architecture":"amd64"`, `"architecture":"arm64"`)
			},
			match: "RuntimeArchitectureMismatch",
		},
		{
			name: "runtime backend incompatible",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				replaceRuntimeDeclaration(
					config,
					`"backends":["CPUExecutionProvider"]`,
					`"backends":["CUDAExecutionProvider"]`,
				)
			},
			match: "RuntimeBackendUnavailable",
		},
		{
			name: "artifact architecture mismatch",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				config.Data["execution-facts.yaml"] = strings.Replace(
					config.Data["execution-facts.yaml"], "      architecture: amd64", "      architecture: arm64", 1,
				)
			},
			match: "artifact supply architecture does not match DeviceClass",
		},
		{
			name: "unsafe artifact path",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				config.Data["execution-facts.yaml"] = strings.Replace(
					config.Data["execution-facts.yaml"], testArtifactPath, "/artifact/../hidden", 1,
				)
			},
			match: "artifact path must be a clean absolute container path",
		},
		{
			name: "unknown execution fact field",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				config.Data["execution-facts.yaml"] += "unknown: value\n"
			},
			match: `unknown field "unknown"`,
		},
		{
			name: "second YAML document",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				config.Data["execution-facts.yaml"] += "---\nschemaVersion: ignored\n"
			},
			match: "exactly one YAML document",
		},
		{
			name: "missing exact declaration",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				config.Data["execution-facts.yaml"] = strings.ReplaceAll(
					config.Data["execution-facts.yaml"], "deviceClass: lattepanda-3-delta-8g", "deviceClass: other-class",
				)
			},
			match: "no trusted execution declaration",
		},
		{
			name: "dangling artifact reference",
			mutate: func(objects []client.Object) {
				objects[0].(*edgev1alpha1.ModelSpec).Spec.ExecutionPaths[0].Artifact = "missing"
			},
			match: "references missing artifact",
		},
		{
			name: "derived artifact missing source",
			mutate: func(objects []client.Object) {
				artifact := &objects[0].(*edgev1alpha1.ModelSpec).Spec.Artifacts[0]
				artifact.Derivation = validArtifactDerivation("missing")
			},
			match: "references missing source artifact",
		},
		{
			name: "derived artifact references itself",
			mutate: func(objects []client.Object) {
				artifact := &objects[0].(*edgev1alpha1.ModelSpec).Spec.Artifacts[0]
				artifact.Derivation = validArtifactDerivation(artifact.Name)
			},
			match: "references itself as source",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := validCandidateObjects()
			test.mutate(objects)
			resolver := newCandidateResolver(t, objects...)
			_, err := resolver.ResolveCandidate(context.Background(), validCandidateSelection())
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestKubernetesCandidateResolverProjectsExactSelectedActuator(t *testing.T) {
	objects := validCandidateObjects()
	class := objects[1].(*edgev1alpha1.DeviceClass)
	class.Spec.PowerModes = append(class.Spec.PowerModes, edgev1alpha1.PowerMode{
		Name: testSelectablePowerMode, Actuator: edgev1alpha1.PowerModeActuator{
			Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
			JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 1},
		},
	})
	selection := validCandidateSelection()
	selection.PowerMode = testSelectablePowerMode
	resolved, err := newCandidateResolver(t, objects...).ResolveCandidate(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.SelectedActuator == nil || resolved.SelectedActuator.JetsonNVPModel == nil ||
		resolved.SelectedActuator.JetsonNVPModel.ID != 1 {
		t.Fatalf("selected actuator = %#v", resolved.SelectedActuator)
	}
	report, err := profilingrun.DecodeCandidateReport(resolved.ReportBytes)
	if err != nil {
		t.Fatal(err)
	}
	if report.Selection.Actuator == nil || report.Selection.Actuator.JetsonNVPModel == nil ||
		report.Selection.Actuator.JetsonNVPModel.ID != 1 {
		t.Fatalf("candidate report actuator = %#v", report.Selection.Actuator)
	}
}

func validArtifactDerivation(source string) *edgev1alpha1.ModelArtifactDerivation {
	return &edgev1alpha1.ModelArtifactDerivation{
		SourceArtifact: source,
		TargetPlatform: "rk3588",
		FixedBatch:     1,
		Input: &edgev1alpha1.ModelArtifactInputContract{
			Shape: []int32{1, 224, 224, 3}, Layout: profilingrun.RKNNLiteInputLayout,
			DataType: profilingrun.RKNNLiteInputDataType,
		},
		Conversion: edgev1alpha1.ModelArtifactConversion{
			Tool:    "rknn-toolkit2",
			Version: "2.3.2",
		},
	}
}

func TestKubernetesCandidateResolverPreservesNotFound(t *testing.T) {
	objects := validCandidateObjects()[1:]
	resolver := newCandidateResolver(t, objects...)
	_, err := resolver.ResolveCandidate(context.Background(), validCandidateSelection())
	if err == nil || !apierrors.IsNotFound(err) || !strings.Contains(err.Error(), `get ModelSpec "mobilenet-v2-050"`) {
		t.Fatalf("expected ModelSpec NotFound cause, got %v", err)
	}
}

func TestNewKubernetesCandidateResolverValidatesDependencies(t *testing.T) {
	if _, err := NewKubernetesCandidateResolver(nil, candidateResolverOptions()); err == nil {
		t.Fatal("expected nil reader rejection")
	}
	objects := validCandidateObjects()
	reader := candidateFakeReader(t, objects...)
	options := candidateResolverOptions()
	options.ConfigMapKey = " "
	if _, err := NewKubernetesCandidateResolver(reader, options); err == nil {
		t.Fatal("expected invalid fact-source options rejection")
	}
}

func newCandidateResolver(t *testing.T, objects ...client.Object) *KubernetesCandidateResolver {
	t.Helper()
	resolver, err := NewKubernetesCandidateResolver(candidateFakeReader(t, objects...), candidateResolverOptions())
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func candidateFakeReader(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func candidateResolverOptions() CandidateResolverOptions {
	return CandidateResolverOptions{
		Namespace: testSystemNamespace, ConfigMapName: "chill-profiler-facts", ConfigMapKey: "execution-facts.yaml",
	}
}

func validCandidateSelection() profiler.CandidateSelection {
	return profiler.CandidateSelection{
		ModelSpec: "mobilenet-v2-050", DeviceClass: "lattepanda-3-delta-8g",
		ExecutionPath: "ort-cpu", PowerMode: fixedPowerMode,
	}
}

func validCandidateObjects() []client.Object {
	model := &edgev1alpha1.ModelSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mobilenet-v2-050", UID: types.UID("model-uid"), ResourceVersion: "10", Generation: 2,
		},
		Spec: edgev1alpha1.ModelSpecSpec{
			Artifacts: []edgev1alpha1.ModelArtifact{{
				Name: "canonical-onnx", Format: "onnx",
			}},
			ExecutionPaths: []edgev1alpha1.ModelExecutionPath{{
				Name: "ort-cpu", Artifact: "canonical-onnx",
				Runtime: edgev1alpha1.ModelRuntimeRequirement{
					Family: "onnxruntime", Backend: "CPUExecutionProvider",
				},
			}},
		},
	}
	deviceClass := &edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "lattepanda-3-delta-8g", UID: types.UID("device-uid"), ResourceVersion: "20", Generation: 3,
		},
		Spec: edgev1alpha1.DeviceClassSpec{
			NodeSelector: map[string]string{"edge.dacs.io/device-class": "lattepanda-3-delta-8g"},
			Architecture: testArchitecture, PowerModes: []edgev1alpha1.PowerMode{{
				Name: fixedPowerMode, Actuator: edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorFixed},
			}},
		},
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testSystemNamespace, Name: "chill-profiler-facts",
			UID: types.UID("config-uid"), ResourceVersion: "30",
		},
		Data: map[string]string{"execution-facts.yaml": validExecutionFacts()},
	}
	return []client.Object{model, deviceClass, configMap}
}

func validRKNNCandidateObjects() []client.Object {
	objects := validCandidateObjects()
	model := objects[0].(*edgev1alpha1.ModelSpec)
	model.Spec.Artifacts = append(model.Spec.Artifacts, edgev1alpha1.ModelArtifact{
		Name: "mobilenet-v2-050-rk3588-bs1", Format: "rknn",
		Derivation: validArtifactDerivation("canonical-onnx"),
	})
	model.Spec.ExecutionPaths = append(model.Spec.ExecutionPaths, edgev1alpha1.ModelExecutionPath{
		Name: "rknnlite", Artifact: "mobilenet-v2-050-rk3588-bs1",
		Runtime: edgev1alpha1.ModelRuntimeRequirement{Family: "rknnlite", Backend: "not-applicable"},
	})
	class := objects[1].(*edgev1alpha1.DeviceClass)
	class.Name = "orangepi-5-plus-16g"
	class.Spec.Architecture = testARM64Architecture
	class.Spec.NodeSelector = map[string]string{"edge.dacs.io/device-class": class.Name}
	objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = validRKNNExecutionFacts()
	return objects
}

func validExecutionFacts() string {
	payload := validRuntimeDeclaration()
	return `schemaVersion: chill.dacs.io/profiler-execution-facts.v1alpha5
metricObserverImage: ` + testMetricObserverImage + `
declarations:
  - modelSpec: mobilenet-v2-050
    deviceClass: lattepanda-3-delta-8g
    executionPath: ort-cpu
    runtime:
      payload: |-
        ` + payload + `
    artifact:
      schemaVersion: chill.dacs.io/artifact-supply.v1alpha1
      image: ` + testArtifactImage + `
      architecture: amd64
      path: /artifact/mobilenet-v2-050.onnx
`
}

func validRKNNExecutionFacts() string {
	payload := validRKNNRuntimeDeclaration()
	return `schemaVersion: chill.dacs.io/profiler-execution-facts.v1alpha5
metricObserverImage: ` + testMetricObserverImage + `
declarations:
  - modelSpec: mobilenet-v2-050
    deviceClass: orangepi-5-plus-16g
    executionPath: rknnlite
    runtime:
      payload: |-
        ` + payload + `
    artifact:
      schemaVersion: chill.dacs.io/artifact-supply.v1alpha1
      image: ` + testArtifactImage + `
      architecture: arm64
      path: /artifact/mobilenet-v2-050-rk3588-bs1.rknn
    host:
      libraries:
        - name: librknnrt
          hostPath: /usr/lib/librknnrt.so
          containerPath: /usr/lib/librknnrt.so
      driver:
        name: rknpu
        versionPath: /sys/module/rknpu/version
        version: 0.9.6
      platform:
        compatiblePath: /proc/device-tree/compatible
        compatibleToken: rockchip,rk3588
      devices:
        - path: /dev/dri/card0
        - path: /dev/dri/card1
        - path: /dev/dri/renderD128
        - path: /dev/dri/renderD129
      allocation:
        resourceName: ""
        exclusivityClaimed: false
    access:
      privileged: true
      supplementalGroups:
        - 44
        - 110
`
}

func validRuntimeDeclaration() string {
	return `{"schemaVersion":"chill.dacs.io/runtime-declaration.v1alpha1","image":"` + testRuntimeImage + `","architecture":"amd64","runtime":{"family":"onnxruntime","backends":["CPUExecutionProvider"]},"verification":"exact-image-runtime-inspection-v1"}`
}

func validRKNNRuntimeDeclaration() string {
	return `{"schemaVersion":"chill.dacs.io/runtime-declaration.v1alpha2","image":"` + testRuntimeImage + `","architecture":"` + testARM64Architecture + `","runtime":{"family":"rknnlite","backends":["not-applicable"]},"packages":[{"name":"rknn-toolkit-lite2","version":"2.3.2"}],"verification":"exact-image-runtime-inspection-v1"}`
}

func replaceRuntimeDeclaration(config *corev1.ConfigMap, old, replacement string) {
	original := validRuntimeDeclaration()
	changed := strings.Replace(original, old, replacement, 1)
	raw := config.Data["execution-facts.yaml"]
	config.Data["execution-facts.yaml"] = strings.Replace(raw, original, changed, 1)
}
