package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConsoleCompatibilityRouteUsesProductionResolver(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	model := &edgev1alpha1.ModelSpec{
		ObjectMeta: metav1.ObjectMeta{Name: "model", UID: "model-uid", ResourceVersion: "10"},
		Spec: edgev1alpha1.ModelSpecSpec{
			Artifacts: []edgev1alpha1.ModelArtifact{{Name: "onnx", Format: "onnx", Source: edgev1alpha1.ModelArtifactSource{
				Image: "registry.example/model:v1", Architecture: "amd64", Files: []string{"/artifact/model.onnx"},
			}}},
			ExecutionPaths: []edgev1alpha1.ModelExecutionPath{{
				Name: "ort-cpu", Artifact: "onnx", Runtime: edgev1alpha1.ModelRuntimeRequirement{
					Family: "firstfloor", Backend: "ort-cpu",
					Settings: &edgev1alpha1.ModelRuntimeSettings{Firstfloor: &edgev1alpha1.FirstfloorRuntimeSettings{
						MaxBatchSize: 1, MaxQueueSize: 1, DeadlinePolicy: "keep",
					}},
				},
			}},
		},
	}
	class := &edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid", ResourceVersion: "11"},
		Spec:       edgev1alpha1.DeviceClassSpec{Architecture: "amd64", NodeSelector: map[string]string{"class": "class"}},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "node", UID: "node-uid", ResourceVersion: "12", Labels: map[string]string{"class": "class"},
	}}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model, class, node).Build()
	handler, err := buildHandler(reader, options{systemName: "chill", namespace: "chill-system", tempDir: t.TempDir(),
		factSourceConfigMap: "facts", executionFactsKey: "execution", metricSourcesKey: "metrics",
		workloadObservationKey: "workload", actuationPoliciesKey: "actuation",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/model-compatibility?modelSpec=model&executionPath=ort-cpu&deviceClass=class&node=node", nil))
	var view operatorconsole.ModelCompatibilityView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || view.Verdict != "Unknown" || len(view.Reasons) != 1 ||
		view.Reasons[0].Code != "RuntimeDeclarationMissing" || view.Inputs.ModelSpec.UID != "model-uid" ||
		view.Inputs.DeviceClass.UID != "class-uid" || view.Inputs.Node.UID != "node-uid" ||
		view.Inputs.RuntimeDeclaration != nil || view.ReadAt == "" || view.Selection.ExecutionPath != "ort-cpu" {
		t.Fatalf("unwired/invented compatibility: %d %s", response.Code, response.Body.String())
	}
}
