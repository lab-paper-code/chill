package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	otherNode := node.DeepCopy()
	otherNode.Name, otherNode.UID = "z-node", "z-node-uid"
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(model, class, node, otherNode).Build()
	reader := &countingCompatibilityReader{Reader: base, gets: make(map[string]int)}
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
	reader.gets = make(map[string]int)
	readMatrix := func() operatorconsole.ModelCompatibilityMatrixView {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/model-compatibility-matrix", nil))
		var matrix operatorconsole.ModelCompatibilityMatrixView
		if err := json.Unmarshal(response.Body.Bytes(), &matrix); err != nil || response.Code != 200 {
			t.Fatalf("matrix: %d %s (%v)", response.Code, response.Body.String(), err)
		}
		return matrix
	}
	matrix := readMatrix()
	if len(matrix.Rows) != 1 || len(matrix.NodeRows) != 2 || matrix.Rows[0].Selection.Node != "node" ||
		matrix.NodeRows[1].Selection.Node != "z-node" || matrix.NodeRows[1].Inputs.Node.UID != "z-node-uid" {
		t.Fatalf("representative/Node rows: %+v", matrix)
	}
	factKey := "*v1.ConfigMap:chill-system/facts"
	if len(reader.gets) != 1 || reader.gets[factKey] != 1 {
		t.Fatalf("LIST seeds were refetched or facts were not memoized: %+v", reader.gets)
	}
	unchanged := readMatrix()
	if unchanged.ReadAt != matrix.ReadAt || unchanged.NodeRows[0].ReadAt != matrix.NodeRows[0].ReadAt || reader.gets[factKey] != 2 {
		t.Fatalf("unchanged source was recomputed or refetched: %+v / %+v", matrix, unchanged)
	}
	// A replacement Node must invalidate support even if names are unchanged.
	if err := base.Delete(context.Background(), otherNode); err != nil {
		t.Fatal(err)
	}
	otherNode.UID, otherNode.ResourceVersion = "replacement-node", ""
	if err := base.Create(context.Background(), otherNode); err != nil {
		t.Fatal(err)
	}
	changed := readMatrix()
	if changed.NodeRows[1].Inputs.Node.UID != "replacement-node" || reader.gets[factKey] != 3 {
		t.Fatalf("replacement reused stale inputs: %+v, gets=%+v", changed, reader.gets)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	query := newModelCompatibilityMatrixQuery(reader, validOptions())
	if _, err := query(ctx); err != context.Canceled {
		t.Fatalf("cancelled matrix = %v", err)
	}
}

type countingCompatibilityReader struct {
	client.Reader
	gets map[string]int
}

func (r *countingCompatibilityReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	r.gets[fmt.Sprintf("%T:%s", object, key)]++
	return r.Reader.Get(ctx, key, object, opts...)
}
