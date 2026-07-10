package system

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

const manualMetadataValue = "keep"

func TestRemoveManagedNodeMetadata(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "edge-1",
			Labels: map[string]string{
				chillmeta.DeviceClass:  "jetson-orin-nano-8g",
				chillmeta.DeviceVendor: "nvidia",
				chillmeta.DeviceFamily: "jetson",
				chillmeta.DeviceModel:  "orin-nano",
				chillmeta.Accelerator:  "nvidia-jetson-orin-nano",
				"manual":               manualMetadataValue,
			},
			Annotations: map[string]string{
				chillmeta.ManagedBy:                  chillmeta.ManagedByDeviceDiscovery,
				chillmeta.DiscoverySource:            chillmeta.SourceNodeDiscovery,
				chillmeta.DeviceModelRaw:             "NVIDIA Jetson Orin Nano",
				chillmeta.NodeDiscoveryResult:        chillmeta.DiscoveryResultMatched,
				chillmeta.NodeDiscoveryReason:        chillmeta.DiscoveryReasonSignatureMatched,
				chillmeta.DeviceClassDiscoveryResult: chillmeta.DiscoveryResultMatched,
				chillmeta.DeviceClassDiscoveryReason: chillmeta.DiscoveryReasonCatalogMatched,
				chillmeta.DeviceClassDiscoveryClass:  "jetson-orin-nano-8g",
				chillmeta.System:                     "chill",
				"manual.edge.dacs.io/annotation":     manualMetadataValue,
			},
		},
	}

	if !removeManagedNodeMetadata(node, "chill") {
		t.Fatal("removeManagedNodeMetadata() changed = false, want true")
	}
	if node.Labels[chillmeta.DeviceClass] != "" || node.Labels[chillmeta.DeviceVendor] != "" {
		t.Fatalf("managed labels were not removed: %#v", node.Labels)
	}
	if node.Labels["manual"] != manualMetadataValue {
		t.Fatalf("manual label = %q, want %s", node.Labels["manual"], manualMetadataValue)
	}
	if node.Annotations[chillmeta.ManagedBy] != "" || node.Annotations[chillmeta.DeviceClassDiscoveryResult] != "" || node.Annotations[chillmeta.System] != "" {
		t.Fatalf("managed annotations were not removed: %#v", node.Annotations)
	}
	if node.Annotations["manual.edge.dacs.io/annotation"] != manualMetadataValue {
		t.Fatalf("manual annotation = %q, want %s", node.Annotations["manual.edge.dacs.io/annotation"], manualMetadataValue)
	}
}

func TestRemoveManagedNodeMetadataSkipsOtherSystems(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "edge-1",
			Labels: map[string]string{
				chillmeta.DeviceClass: "jetson-orin-nano-8g",
			},
			Annotations: map[string]string{
				chillmeta.ManagedBy: chillmeta.ManagedByDeviceDiscovery,
				chillmeta.System:    "other-system",
			},
		},
	}

	if removeManagedNodeMetadata(node, "chill") {
		t.Fatal("removeManagedNodeMetadata() changed = true, want false for another system")
	}
	if node.Labels[chillmeta.DeviceClass] != "jetson-orin-nano-8g" {
		t.Fatalf("DeviceClass label = %q, want preserved", node.Labels[chillmeta.DeviceClass])
	}
}

func TestFinalizeDeletesDeviceClassesAndCleansNodes(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(apps) error = %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core) error = %v", err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(edge) error = %v", err)
	}

	system := &edgev1alpha1.ChillSystem{
		ObjectMeta: metav1.ObjectMeta{Name: "chill"},
		Spec: edgev1alpha1.ChillSystemSpec{
			ManagementNamespace: "chill-system",
			NodeDiscovery: edgev1alpha1.ChillNodeDiscoverySpec{
				DaemonSetName: "chill-node-discovery",
			},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "edge-1",
			Labels: map[string]string{
				chillmeta.DeviceClass: "jetson-orin-nano-8g",
				"manual":              manualMetadataValue,
			},
			Annotations: map[string]string{
				chillmeta.ManagedBy: chillmeta.ManagedByDeviceDiscovery,
				chillmeta.System:    "chill",
			},
		},
	}
	deviceClass := &edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "manual-or-discovered",
			Labels: map[string]string{
				chillmeta.System: "chill",
			},
		},
		Spec: edgev1alpha1.DeviceClassSpec{
			NodeSelector: map[string]string{chillmeta.DeviceClass: "manual-or-discovered"},
			Architecture: "arm64",
			MemoryBytes:  resource.MustParse("1Gi"),
			Accelerator:  "none",
			PowerModes:   []edgev1alpha1.PowerMode{{Name: "fixed"}},
		},
	}
	otherDeviceClass := deviceClass.DeepCopy()
	otherDeviceClass.Name = "other-system-class"
	otherDeviceClass.Labels = map[string]string{chillmeta.System: "other-system"}
	reconciler := &ChillSystemReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, node, deviceClass, otherDeviceClass).Build(),
		Options: Options{
			Namespace: "chill-system",
		},
	}

	done, err := reconciler.finalize(ctx, system)
	if err != nil {
		t.Fatalf("finalize() error = %v", err)
	}
	if !done {
		t.Fatal("finalize() done = false, want true when DaemonSet is already gone")
	}

	updatedNode := &corev1.Node{}
	if err := reconciler.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode); err != nil {
		t.Fatalf("Get(Node) error = %v", err)
	}
	if _, ok := updatedNode.Labels[chillmeta.DeviceClass]; ok {
		t.Fatalf("DeviceClass label still exists after finalize: %#v", updatedNode.Labels)
	}
	if updatedNode.Labels["manual"] != manualMetadataValue {
		t.Fatalf("manual label = %q, want %s", updatedNode.Labels["manual"], manualMetadataValue)
	}

	remaining := &edgev1alpha1.DeviceClass{}
	if err := reconciler.Get(ctx, types.NamespacedName{Name: deviceClass.Name}, remaining); err == nil {
		t.Fatal("DeviceClass still exists after finalize")
	}
	if err := reconciler.Get(ctx, types.NamespacedName{Name: otherDeviceClass.Name}, remaining); err != nil {
		t.Fatalf("other-system DeviceClass was deleted: %v", err)
	}
}
