package deviceclass

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDiscoverCatalogMatch(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1ObjectMeta(map[string]string{
			metadata.DeviceModel: "orin-nano",
		}),
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{Architecture: "arm64"},
		},
	}
	catalog := Catalog{
		Classes: []CatalogEntry{
			{
				Name: "jetson-orin-nano-8g",
				MatchLabels: map[string]string{
					metadata.DeviceModel: "orin-nano",
				},
				Architecture: "arm64",
				Memory:       resource.MustParse("8Gi"),
				Accelerator:  "nvidia-jetson-orin-nano",
				PowerModes: []edgev1alpha1.PowerMode{
					{Name: "15W"},
				},
			},
		},
	}

	discovered, ok, err := Discover(node, catalog, Options{
		LabelKey:            metadata.DeviceClass,
		RequireCatalogMatch: true,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !ok {
		t.Fatal("Discover() ok = false, want true")
	}
	if discovered.Name != "jetson-orin-nano-8g" {
		t.Fatalf("Name = %q, want jetson-orin-nano-8g", discovered.Name)
	}
	if discovered.Spec.NodeSelector[metadata.DeviceClass] != "jetson-orin-nano-8g" {
		t.Fatalf("NodeSelector = %#v", discovered.Spec.NodeSelector)
	}
}

func TestDiscoverSkipsUnmatchedNodeWhenCatalogRequired(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1ObjectMeta(map[string]string{
			metadata.DeviceModel: "unknown",
		}),
	}
	catalog := Catalog{
		Classes: []CatalogEntry{
			{
				Name: "jetson-orin-nano-8g",
				MatchLabels: map[string]string{
					metadata.DeviceModel: "orin-nano",
				},
				PowerModes: []edgev1alpha1.PowerMode{{Name: "15W"}},
			},
		},
	}

	_, ok, err := Discover(node, catalog, Options{RequireCatalogMatch: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if ok {
		t.Fatal("Discover() ok = true, want false")
	}
}

func TestDiscoverInfersUnmatchedNodeWithFallbackPowerModes(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1ObjectMeta(map[string]string{
			metadata.DeviceModel: "custom-board",
		}),
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
			NodeInfo: corev1.NodeSystemInfo{Architecture: "arm64"},
		},
	}

	discovered, ok, err := Discover(node, Catalog{}, Options{
		RequireCatalogMatch: false,
		FallbackPowerModes:  []edgev1alpha1.PowerMode{{Name: "fixed"}},
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !ok {
		t.Fatal("Discover() ok = false, want true")
	}
	if discovered.Name != "custom-board-2g" {
		t.Fatalf("Name = %q, want custom-board-2g", discovered.Name)
	}
	if len(discovered.Spec.PowerModes) != 1 || discovered.Spec.PowerModes[0].Name != "fixed" {
		t.Fatalf("PowerModes = %#v, want fixed fallback", discovered.Spec.PowerModes)
	}
}

func TestDiscoverRequiresFallbackPowerModesWhenCatalogOptional(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1ObjectMeta(map[string]string{
			metadata.DeviceModel: "custom-board",
		}),
	}

	_, ok, err := Discover(node, Catalog{}, Options{RequireCatalogMatch: false})
	if err == nil {
		t.Fatal("Discover() error = nil, want fallback power mode validation error")
	}
	if ok {
		t.Fatal("Discover() ok = true, want false")
	}
}

func TestSpecEqualUsesQuantitySemanticEquality(t *testing.T) {
	a := edgev1alpha1.DeviceClassSpec{
		NodeSelector: map[string]string{metadata.DeviceClass: "class-a"},
		Architecture: "arm64",
		MemoryBytes:  resource.MustParse("1024Mi"),
		PowerModes:   []edgev1alpha1.PowerMode{{Name: "fixed"}},
	}
	b := edgev1alpha1.DeviceClassSpec{
		NodeSelector: map[string]string{metadata.DeviceClass: "class-a"},
		Architecture: "arm64",
		MemoryBytes:  resource.MustParse("1Gi"),
		PowerModes:   []edgev1alpha1.PowerMode{{Name: "fixed"}},
	}

	if !SpecEqual(a, b) {
		t.Fatal("SpecEqual() = false, want true")
	}
}

func metav1ObjectMeta(labels map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Labels: labels}
}
