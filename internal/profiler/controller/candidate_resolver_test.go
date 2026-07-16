package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	testArtifactDigest     = "sha256:8645e5d6511cf0f78fa4a451e3bd86b3ab6b39bb5f9216ba32d2d9aebc852ee2"
	testArtifactPath       = "/artifact/mobilenet-v2-050.onnx"
	testArchitecture       = "amd64"
	testRuntimeImage       = "registry.example/chill/runtime@sha256:1aa023ab6fb43fd8f8a978036878849b7a5a0a2f0621e510b534fc767ea5774b"
	testArtifactImage      = "registry.example/chill/artifact@sha256:8cf7dea5f1b9cb00fcb1511c0f63c187e3d1cceaa44038e1c705d61f0714b2aa"
	testPowerObserverImage = "registry.example/chill/power-observer@sha256:522325aaeb2ce2e2fea91372ebd8d931c1507c73687e4d20d70add6714c5077c"
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

func assertResolvedCandidate(t *testing.T, resolved profiler.ResolvedCandidate, selection profiler.CandidateSelection) {
	t.Helper()
	if resolved.Verdict != "Compatible" || resolved.State.PowerMode != fixedPowerMode ||
		resolved.State.ArtifactPath != testArtifactPath ||
		resolved.RuntimeImage != testRuntimeImage || resolved.ArtifactImage != testArtifactImage ||
		resolved.ArtifactArchitecture != testArchitecture || resolved.ArtifactPath != testArtifactPath ||
		resolved.ArtifactDigest != testArtifactDigest ||
		resolved.PowerObserverImage != testPowerObserverImage ||
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
	reportSum := sha256.Sum256(resolved.ReportBytes)
	intent := profilingrun.Intent{
		Candidate: profilingrun.CandidateIdentity{
			ReportDigest:             "sha256:" + hex.EncodeToString(reportSum[:]),
			ModelSpecContentDigest:   resolved.ModelSpecContentDigest,
			DeviceClassContentDigest: resolved.DeviceClassContentDigest,
			RuntimeDeclarationDigest: resolved.RuntimeDeclarationContentDigest,
			ExecutionPath:            selection.ExecutionPath,
			Verdict:                  resolved.Verdict,
		},
		State: resolved.State,
		Images: profilingrun.Images{
			Runtime: resolved.RuntimeImage, Artifact: resolved.ArtifactImage,
			PowerObserver: resolved.PowerObserverImage,
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
		report.Inputs.RuntimeDeclaration.Namespace != "chill-system" ||
		report.Inputs.RuntimeDeclaration.Name != "chill-profiler-facts" ||
		report.Inputs.RuntimeDeclaration.UID != "config-uid" ||
		report.Inputs.RuntimeDeclaration.ResourceVersion != "30" ||
		report.Inputs.RuntimeDeclaration.Key != "execution-facts.yaml" ||
		report.Selection.PowerMode != fixedPowerMode ||
		report.DeviceClassFacts.Architecture != testArchitecture ||
		report.DeviceClassFacts.NodeSelector["edge.dacs.io/device-class"] != selection.DeviceClass ||
		report.ArtifactSupply.Image != testArtifactImage ||
		report.ArtifactSupply.Path != testArtifactPath ||
		report.ArtifactSupply.Digest != testArtifactDigest {
		t.Fatalf("candidate report lost source identity: %#v", report)
	}
}

func TestKubernetesCandidateResolverIsDeterministicAndContentSensitive(t *testing.T) {
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
	if string(first.ReportBytes) != string(second.ReportBytes) ||
		first.ModelSpecContentDigest != second.ModelSpecContentDigest {
		t.Fatal("identical Kubernetes snapshots produced different candidates")
	}

	changedObjects := validCandidateObjects()
	changedObjects[0].(*edgev1alpha1.ModelSpec).Generation = 3
	changed := newCandidateResolver(t, changedObjects...)
	third, err := changed.ResolveCandidate(context.Background(), validCandidateSelection())
	if err != nil {
		t.Fatal(err)
	}
	if first.ModelSpecContentDigest == third.ModelSpecContentDigest || string(first.ReportBytes) == string(third.ReportBytes) {
		t.Fatal("changed ModelSpec content retained the previous candidate identity")
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
				objects[1].(*edgev1alpha1.DeviceClass).Spec.PowerModes[0].Name = "15W"
			},
			match: "not the first-path non-selectable fixed-power class",
		},
		{
			name: "selectable mode mixed with fixed",
			mutate: func(objects []client.Object) {
				class := objects[1].(*edgev1alpha1.DeviceClass)
				id := 0
				class.Spec.PowerModes = append(class.Spec.PowerModes, edgev1alpha1.PowerMode{Name: "15W", NvpID: &id})
			},
			match: "not the first-path non-selectable fixed-power class",
		},
		{
			name: "fixed mode has actuator identity",
			mutate: func(objects []client.Object) {
				id := 0
				objects[1].(*edgev1alpha1.DeviceClass).Spec.PowerModes[0].NvpID = &id
			},
			match: "not the first-path non-selectable fixed-power class",
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
			name: "mutable runtime image",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				replaceRuntimeDeclaration(config, testRuntimeImage, "registry.example/chill/runtime:latest")
			},
			match: "runtime image must be digest-pinned",
		},
		{
			name: "mutable power-observer image",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				config.Data["execution-facts.yaml"] = strings.Replace(
					config.Data["execution-facts.yaml"], testPowerObserverImage, "registry.example/chill/power-observer:latest", 1,
				)
			},
			match: "power-observer image must be digest-pinned",
		},
		{
			name: "runtime producer digest mismatch",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				config.Data["execution-facts.yaml"] = strings.Replace(
					config.Data["execution-facts.yaml"], "contentDigest: sha256:", "contentDigest: sha256:f", 1,
				)
			},
			match: "runtime producer declaration content digest mismatch",
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
			name: "artifact digest mismatch",
			mutate: func(objects []client.Object) {
				config := objects[2].(*corev1.ConfigMap)
				config.Data["execution-facts.yaml"] = strings.Replace(
					config.Data["execution-facts.yaml"], testArtifactDigest, "sha256:"+strings.Repeat("f", 64), 1,
				)
			},
			match: "artifact supply digest does not match ModelSpec artifact",
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
		Namespace: "chill-system", ConfigMapName: "chill-profiler-facts", ConfigMapKey: "execution-facts.yaml",
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
				Name: "canonical-onnx", Format: "onnx", Digest: testArtifactDigest,
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
			Architecture: testArchitecture, PowerModes: []edgev1alpha1.PowerMode{{Name: fixedPowerMode}},
		},
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: "chill-profiler-facts",
			UID: types.UID("config-uid"), ResourceVersion: "30",
		},
		Data: map[string]string{"execution-facts.yaml": validExecutionFacts()},
	}
	return []client.Object{model, deviceClass, configMap}
}

func validExecutionFacts() string {
	payload := validRuntimeDeclaration()
	digest := sha256.Sum256([]byte(payload))
	return `schemaVersion: chill.dacs.io/profiler-execution-facts.v1alpha1
powerObserverImage: ` + testPowerObserverImage + `
declarations:
  - modelSpec: mobilenet-v2-050
    deviceClass: lattepanda-3-delta-8g
    executionPath: ort-cpu
    runtime:
      contentDigest: sha256:` + hex.EncodeToString(digest[:]) + `
      payload: |-
        ` + payload + `
    artifact:
      schemaVersion: chill.dacs.io/artifact-supply.v1alpha1
      image: ` + testArtifactImage + `
      architecture: amd64
      path: /artifact/mobilenet-v2-050.onnx
      digest: ` + testArtifactDigest + `
`
}

func validRuntimeDeclaration() string {
	return `{"schemaVersion":"spikes.chill.dacs.io/runtime-declaration.v1alpha1","image":"` + testRuntimeImage + `","architecture":"amd64","runtime":{"family":"onnxruntime","backends":["CPUExecutionProvider"]},"verification":"exact-image-runtime-inspection-v1"}`
}

func replaceRuntimeDeclaration(config *corev1.ConfigMap, old, replacement string) {
	original := validRuntimeDeclaration()
	changed := strings.Replace(original, old, replacement, 1)
	originalSum := sha256.Sum256([]byte(original))
	changedSum := sha256.Sum256([]byte(changed))
	raw := config.Data["execution-facts.yaml"]
	raw = strings.Replace(raw, hex.EncodeToString(originalSum[:]), hex.EncodeToString(changedSum[:]), 1)
	config.Data["execution-facts.yaml"] = strings.Replace(raw, original, changed, 1)
}
