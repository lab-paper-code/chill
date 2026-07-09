package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/lab-paper-code/chill/internal/metadata"
)

func TestBuildNodePatchPreservesExistingMetadata(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"kubernetes.io/hostname": "edge-01",
				metadata.DeviceClass:     "manual-class",
			},
			Annotations: map[string]string{
				"existing": "value",
			},
		},
	}

	patch, changed, err := buildNodePatch(node, map[string]string{
		metadata.DeviceModel: "orin-nano",
	}, map[string]string{
		metadata.DiscoverySource: metadata.SourceNodeDiscovery,
	})
	if err != nil {
		t.Fatalf("buildNodePatch() error = %v", err)
	}
	if !changed {
		t.Fatal("buildNodePatch() changed = false, want true")
	}

	var payload struct {
		Metadata struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(patch, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, ok := payload.Metadata.Labels[metadata.DeviceClass]; ok {
		t.Fatalf("device class label was included in node-discovery patch")
	}
	if payload.Metadata.Labels[metadata.DeviceModel] != "orin-nano" {
		t.Fatalf("device model label = %q, want orin-nano", payload.Metadata.Labels[metadata.DeviceModel])
	}
	if _, ok := payload.Metadata.Annotations["existing"]; ok {
		t.Fatalf("unmanaged annotation was included in node-discovery patch")
	}
}

func TestRunOnceAnnotatesNoSourceFacts(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "edge-01"},
	})
	signatureFile := filepath.Join(t.TempDir(), "signatures.yaml")
	if err := os.WriteFile(signatureFile, []byte("sources: []\nsignatures: []\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := runOnce(ctx, clientset, "edge-01", t.TempDir(), signatureFile); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}

	node, err := clientset.CoreV1().Nodes().Get(ctx, "edge-01", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if node.Annotations[metadata.NodeDiscoveryResult] != metadata.DiscoveryResultUnmatched {
		t.Fatalf(
			"node discovery result = %q, want %q",
			node.Annotations[metadata.NodeDiscoveryResult],
			metadata.DiscoveryResultUnmatched,
		)
	}
	if node.Annotations[metadata.NodeDiscoveryReason] != metadata.DiscoveryReasonNoSourceFacts {
		t.Fatalf(
			"node discovery reason = %q, want %q",
			node.Annotations[metadata.NodeDiscoveryReason],
			metadata.DiscoveryReasonNoSourceFacts,
		)
	}
	if _, ok := node.Labels[metadata.DeviceClass]; ok {
		t.Fatalf("node-discovery set DeviceClass label")
	}
}

func TestBuildNodeCleanupPatchRemovesOnlyCHILLManagedMetadata(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				metadata.DeviceVendor: "nvidia",
				metadata.DeviceFamily: "jetson",
				metadata.DeviceModel:  "orin-nano",
				metadata.Accelerator:  "nvidia-jetson-orin-nano",
				metadata.DeviceClass:  "jetson-orin-nano-8g",
				"keep":                "true",
			},
			Annotations: map[string]string{
				metadata.DiscoverySource:            metadata.SourceNodeDiscovery,
				metadata.NodeDiscoveryResult:        metadata.DiscoveryResultMatched,
				metadata.NodeDiscoveryReason:        metadata.DiscoveryReasonSignatureMatched,
				metadata.ManagedBy:                  metadata.ManagedByDeviceDiscovery,
				metadata.DeviceClassDiscoveryResult: metadata.DiscoveryResultMatched,
				metadata.DeviceClassDiscoveryReason: metadata.DiscoveryReasonCatalogMatched,
				metadata.DeviceClassDiscoveryClass:  "jetson-orin-nano-8g",
				metadata.DeviceModelRaw:             "NVIDIA Jetson Orin Nano",
				"keep":                              "true",
			},
		},
	}

	patch, changed, err := buildNodeCleanupPatch(node)
	if err != nil {
		t.Fatalf("buildNodeCleanupPatch() error = %v", err)
	}
	if !changed {
		t.Fatal("buildNodeCleanupPatch() changed = false, want true")
	}

	var payload struct {
		Metadata struct {
			Labels      map[string]*string `json:"labels"`
			Annotations map[string]*string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(patch, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, key := range []string{
		metadata.DeviceVendor,
		metadata.DeviceFamily,
		metadata.DeviceModel,
		metadata.Accelerator,
		metadata.DeviceClass,
	} {
		if _, ok := payload.Metadata.Labels[key]; !ok {
			t.Fatalf("cleanup patch missing label delete for %s", key)
		}
	}
	if _, ok := payload.Metadata.Labels["keep"]; ok {
		t.Fatalf("cleanup patch deletes unrelated label")
	}
	for _, key := range []string{
		metadata.DiscoverySource,
		metadata.NodeDiscoveryResult,
		metadata.NodeDiscoveryReason,
		metadata.ManagedBy,
		metadata.DeviceClassDiscoveryResult,
		metadata.DeviceClassDiscoveryReason,
		metadata.DeviceClassDiscoveryClass,
		metadata.DeviceModelRaw,
	} {
		if _, ok := payload.Metadata.Annotations[key]; !ok {
			t.Fatalf("cleanup patch missing annotation delete for %s", key)
		}
	}
	if _, ok := payload.Metadata.Annotations["keep"]; ok {
		t.Fatalf("cleanup patch deletes unrelated annotation")
	}
}

func TestBuildNodeCleanupPatchPreservesManualDeviceClassLabel(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				metadata.DeviceClass: "manual-class",
			},
			Annotations: map[string]string{
				metadata.DeviceClassDiscoveryResult: metadata.DiscoveryResultMatched,
				metadata.DeviceClassDiscoveryReason: metadata.DiscoveryReasonManualLabelPreserved,
				metadata.DeviceClassDiscoveryClass:  "jetson-orin-nano-8g",
			},
		},
	}

	patch, changed, err := buildNodeCleanupPatch(node)
	if err != nil {
		t.Fatalf("buildNodeCleanupPatch() error = %v", err)
	}
	if !changed {
		t.Fatal("buildNodeCleanupPatch() changed = false, want true")
	}

	var payload struct {
		Metadata struct {
			Labels      map[string]*string `json:"labels"`
			Annotations map[string]*string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(patch, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, ok := payload.Metadata.Labels[metadata.DeviceClass]; ok {
		t.Fatalf("cleanup patch deletes manual device-class label")
	}
	if _, ok := payload.Metadata.Annotations[metadata.DeviceClassDiscoveryResult]; !ok {
		t.Fatalf("cleanup patch should remove stale discovery status")
	}
}
