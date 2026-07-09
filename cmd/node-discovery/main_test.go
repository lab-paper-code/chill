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

	"github.com/lab-paper-code/chill/internal/labels"
)

func TestBuildNodePatchPreservesExistingMetadata(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"kubernetes.io/hostname": "edge-01",
				labels.DeviceClass:       "manual-class",
			},
			Annotations: map[string]string{
				"existing": "value",
			},
		},
	}

	patch, changed, err := buildNodePatch(node, map[string]string{
		labels.DeviceModel: "orin-nano",
	}, map[string]string{
		labels.DiscoverySource: labels.SourceNodeDiscovery,
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
	if _, ok := payload.Metadata.Labels[labels.DeviceClass]; ok {
		t.Fatalf("device class label was included in node-discovery patch")
	}
	if payload.Metadata.Labels[labels.DeviceModel] != "orin-nano" {
		t.Fatalf("device model label = %q, want orin-nano", payload.Metadata.Labels[labels.DeviceModel])
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
	if node.Annotations[labels.NodeDiscoveryResult] != labels.DiscoveryResultUnmatched {
		t.Fatalf(
			"node discovery result = %q, want %q",
			node.Annotations[labels.NodeDiscoveryResult],
			labels.DiscoveryResultUnmatched,
		)
	}
	if node.Annotations[labels.NodeDiscoveryReason] != labels.DiscoveryReasonNoSourceFacts {
		t.Fatalf(
			"node discovery reason = %q, want %q",
			node.Annotations[labels.NodeDiscoveryReason],
			labels.DiscoveryReasonNoSourceFacts,
		)
	}
	if _, ok := node.Labels[labels.DeviceClass]; ok {
		t.Fatalf("node-discovery set DeviceClass label")
	}
}

func TestBuildNodeCleanupPatchRemovesOnlyCHILLManagedMetadata(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				labels.DeviceVendor: "nvidia",
				labels.DeviceFamily: "jetson",
				labels.DeviceModel:  "orin-nano",
				labels.Accelerator:  "nvidia-jetson-orin-nano",
				labels.DeviceClass:  "jetson-orin-nano-8g",
				"keep":              "true",
			},
			Annotations: map[string]string{
				labels.DiscoverySource:            labels.SourceNodeDiscovery,
				labels.NodeDiscoveryResult:        labels.DiscoveryResultMatched,
				labels.NodeDiscoveryReason:        labels.DiscoveryReasonSignatureMatched,
				labels.ManagedBy:                  labels.ManagedByDeviceDiscovery,
				labels.DeviceClassDiscoveryResult: labels.DiscoveryResultMatched,
				labels.DeviceClassDiscoveryReason: labels.DiscoveryReasonCatalogMatched,
				labels.DeviceClassDiscoveryClass:  "jetson-orin-nano-8g",
				labels.DeviceModelRaw:             "NVIDIA Jetson Orin Nano",
				"keep":                            "true",
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
		labels.DeviceVendor,
		labels.DeviceFamily,
		labels.DeviceModel,
		labels.Accelerator,
		labels.DeviceClass,
	} {
		if _, ok := payload.Metadata.Labels[key]; !ok {
			t.Fatalf("cleanup patch missing label delete for %s", key)
		}
	}
	if _, ok := payload.Metadata.Labels["keep"]; ok {
		t.Fatalf("cleanup patch deletes unrelated label")
	}
	for _, key := range []string{
		labels.DiscoverySource,
		labels.NodeDiscoveryResult,
		labels.NodeDiscoveryReason,
		labels.ManagedBy,
		labels.DeviceClassDiscoveryResult,
		labels.DeviceClassDiscoveryReason,
		labels.DeviceClassDiscoveryClass,
		labels.DeviceModelRaw,
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
				labels.DeviceClass: "manual-class",
			},
			Annotations: map[string]string{
				labels.DeviceClassDiscoveryResult: labels.DiscoveryResultMatched,
				labels.DeviceClassDiscoveryReason: labels.DiscoveryReasonManualLabelPreserved,
				labels.DeviceClassDiscoveryClass:  "jetson-orin-nano-8g",
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
	if _, ok := payload.Metadata.Labels[labels.DeviceClass]; ok {
		t.Fatalf("cleanup patch deletes manual device-class label")
	}
	if _, ok := payload.Metadata.Annotations[labels.DeviceClassDiscoveryResult]; !ok {
		t.Fatalf("cleanup patch should remove stale discovery status")
	}
}
