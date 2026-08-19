package kube

import (
	"context"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesNodeResolverFreezesExactMatchingNode(t *testing.T) {
	resolver := newNodeResolver(t, validNode())

	snapshot, err := resolver.ResolveNode(context.Background(), validNodeSelection())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Name != "lattepanda" || snapshot.UID != "node-uid" ||
		snapshot.ResourceVersion != "17" || snapshot.Architecture != "amd64" ||
		snapshot.AllocatableCPU != "4" {
		t.Fatalf("unexpected Node snapshot: %#v", snapshot)
	}
}

func TestKubernetesNodeResolverFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		selection func() profiler.NodeSelection
		mutate    func(*corev1.Node)
		match     string
	}{
		{
			name: "invalid selector", selection: func() profiler.NodeSelection {
				selection := validNodeSelection()
				selection.DeviceClassSelector = map[string]string{"bad key": "value"}
				return selection
			}, match: "validate resolved DeviceClass selector",
		},
		{
			name: "class mismatch", mutate: func(node *corev1.Node) {
				node.Labels["edge.dacs.io/device-class"] = "other-class"
			}, match: "does not match resolved DeviceClass selector",
		},
		{
			name: "architecture mismatch", mutate: func(node *corev1.Node) {
				node.Labels[corev1.LabelArchStable] = testARM64Architecture
			}, match: "does not match resolved DeviceClass architecture",
		},
		{
			name: "missing identity", mutate: func(node *corev1.Node) {
				node.UID = ""
			}, match: "incomplete API identity",
		},
		{
			name: "missing allocatable CPU", mutate: func(node *corev1.Node) {
				node.Status.Allocatable = nil
			}, match: "no positive allocatable CPU",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			node := validNode()
			if test.mutate != nil {
				test.mutate(node)
			}
			resolver := newNodeResolver(t, node)
			selection := validNodeSelection()
			if test.selection != nil {
				selection = test.selection()
			}
			_, err := resolver.ResolveNode(context.Background(), selection)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestKubernetesNodeResolverPreservesNotFound(t *testing.T) {
	resolver := newNodeResolver(t)
	_, err := resolver.ResolveNode(context.Background(), validNodeSelection())
	if err == nil || !apierrors.IsNotFound(err) ||
		!strings.Contains(err.Error(), `get selected Node "lattepanda"`) {
		t.Fatalf("expected selected Node lookup failure, got %v", err)
	}
}

func TestNewKubernetesNodeResolverRequiresReader(t *testing.T) {
	if _, err := NewKubernetesNodeResolver(nil); err == nil {
		t.Fatal("expected nil Kubernetes reader rejection")
	}
}

func newNodeResolver(t *testing.T, objects ...runtime.Object) *KubernetesNodeResolver {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	resolver, err := NewKubernetesNodeResolver(reader)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func validNodeSelection() profiler.NodeSelection {
	return profiler.NodeSelection{
		Name:                "lattepanda",
		DeviceArchitecture:  "amd64",
		DeviceClassSelector: map[string]string{"edge.dacs.io/device-class": "lattepanda-3-delta-8g"},
	}
}

func validNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "lattepanda", UID: types.UID("node-uid"), ResourceVersion: "17",
			Labels: map[string]string{
				corev1.LabelArchStable:      "amd64",
				"edge.dacs.io/device-class": "lattepanda-3-delta-8g",
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
		},
	}
}
