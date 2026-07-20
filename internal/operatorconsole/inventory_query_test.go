package operatorconsole

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNewKubernetesInventoryQueryRequiresReader(t *testing.T) {
	if _, err := NewKubernetesInventoryQuery(nil); err == nil {
		t.Fatal("NewKubernetesInventoryQuery() error = nil")
	}
}

func TestKubernetesInventoryQueryReadsAndProjectsAuthorities(t *testing.T) {
	scheme := inventoryScheme(t)
	class := &edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "orin", UID: "class-uid"},
		Spec: edgev1alpha1.DeviceClassSpec{
			NodeSelector: map[string]string{"edge.dacs.io/device-class": "orin"},
			Architecture: "arm64",
			PowerModes: []edgev1alpha1.PowerMode{{Name: "15W", Actuator: edgev1alpha1.PowerModeActuator{
				Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
				JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 0},
			}}},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orin-1", UID: "node-uid",
			Labels: map[string]string{"edge.dacs.io/device-class": "orin"},
		},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: corev1.ConditionTrue,
		}}},
	}
	model := &edgev1alpha1.ModelSpec{
		ObjectMeta: metav1.ObjectMeta{Name: "mobilenet", UID: "model-uid"},
		Spec: edgev1alpha1.ModelSpecSpec{
			Artifacts: []edgev1alpha1.ModelArtifact{{
				Name: "canonical-onnx", Format: "onnx", Digest: "sha256:" + strings.Repeat("a", 64),
			}},
			ExecutionPaths: []edgev1alpha1.ModelExecutionPath{{
				Name: "ort-cpu", Artifact: "canonical-onnx",
				Runtime: edgev1alpha1.ModelRuntimeRequirement{Family: "onnxruntime", Backend: "CPUExecutionProvider"},
			}},
		},
	}
	reader := &resourceVersionReader{
		Reader:                       fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, node, model).Build(),
		deviceClassesResourceVersion: "classes-rv",
		nodesResourceVersion:         "nodes-rv",
		modelSpecsResourceVersion:    "models-rv",
	}
	query, err := NewKubernetesInventoryQuery(reader)
	if err != nil {
		t.Fatalf("NewKubernetesInventoryQuery() error = %v", err)
	}
	readAt := time.Date(2026, 7, 20, 1, 2, 3, 0, time.FixedZone("KST", 9*60*60))
	query.now = func() time.Time { return readAt }

	got, err := query.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !got.ReadAt.Equal(readAt.UTC()) || got.ReadAt.Location() != time.UTC {
		t.Fatalf("ReadAt = %v", got.ReadAt)
	}
	if got.DeviceClassesResourceVersion != "classes-rv" || got.NodesResourceVersion != "nodes-rv" ||
		got.ModelSpecsResourceVersion != "models-rv" {
		t.Fatalf("list resourceVersions = %q, %q, %q", got.DeviceClassesResourceVersion,
			got.NodesResourceVersion, got.ModelSpecsResourceVersion)
	}
	if len(got.Classes) != 1 || got.Classes[0].Name != "orin" ||
		got.Classes[0].Spec.Architecture != "arm64" || len(got.Classes[0].Nodes) != 1 ||
		got.Classes[0].Nodes[0].Name != "orin-1" || got.Classes[0].Nodes[0].Ready != corev1.ConditionTrue {
		t.Fatalf("Classes = %#v", got.Classes)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "mobilenet" ||
		got.Models[0].Spec.ExecutionPaths[0].Runtime.Family != "onnxruntime" {
		t.Fatalf("Models = %#v", got.Models)
	}
}

func TestKubernetesInventoryQueryReportsAuthorityReadFailures(t *testing.T) {
	want := errors.New("API unavailable")
	tests := []struct {
		name    string
		failFor string
		message string
	}{
		{name: "DeviceClasses", failFor: "classes", message: "list DeviceClasses for inventory"},
		{name: "Nodes", failFor: "nodes", message: "list Nodes for inventory"},
		{name: "ModelSpecs", failFor: "models", message: "list ModelSpecs for inventory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := NewKubernetesInventoryQuery(&failingInventoryReader{err: want, failFor: tt.failFor})
			if err != nil {
				t.Fatalf("NewKubernetesInventoryQuery() error = %v", err)
			}

			_, err = query.Read(context.Background())
			if !errors.Is(err, want) || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("Read() error = %v", err)
			}
		})
	}
}

func TestKubernetesInventoryQueryReportsInvalidStoredSelector(t *testing.T) {
	scheme := inventoryScheme(t)
	class := &edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid"},
		Spec: edgev1alpha1.DeviceClassSpec{
			NodeSelector: map[string]string{"bad key": "value"},
			Architecture: "arm64",
		},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class).Build()
	query, err := NewKubernetesInventoryQuery(reader)
	if err != nil {
		t.Fatalf("NewKubernetesInventoryQuery() error = %v", err)
	}

	_, err = query.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "project inventory") {
		t.Fatalf("Read() error = %v", err)
	}
}

func inventoryScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add CHILL API to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	return scheme
}

type failingInventoryReader struct {
	err     error
	failFor string
}

func (r *failingInventoryReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return nil
}

func (r *failingInventoryReader) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	switch list.(type) {
	case *edgev1alpha1.DeviceClassList:
		if r.failFor == "classes" {
			return r.err
		}
	case *corev1.NodeList:
		if r.failFor == "nodes" {
			return r.err
		}
	case *edgev1alpha1.ModelSpecList:
		if r.failFor == "models" {
			return r.err
		}
	}
	return nil
}

type resourceVersionReader struct {
	client.Reader
	deviceClassesResourceVersion string
	nodesResourceVersion         string
	modelSpecsResourceVersion    string
}

func (r *resourceVersionReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return err
	}
	switch typed := list.(type) {
	case *edgev1alpha1.DeviceClassList:
		typed.ResourceVersion = r.deviceClassesResourceVersion
	case *corev1.NodeList:
		typed.ResourceVersion = r.nodesResourceVersion
	case *edgev1alpha1.ModelSpecList:
		typed.ResourceVersion = r.modelSpecsResourceVersion
	}
	return nil
}
