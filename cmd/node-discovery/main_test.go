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

	"github.com/lab-paper-code/chill/internal/chilllabels"
)

func TestBuildNodePatchPreservesExistingMetadata(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"kubernetes.io/hostname": "edge-01",
				chilllabels.DeviceClass:  "manual-class",
			},
			Annotations: map[string]string{
				"existing": "value",
			},
		},
	}

	patch, changed, err := buildNodePatch(node, map[string]string{
		chilllabels.DeviceModel: "orin-nano",
	}, map[string]string{
		chilllabels.DiscoverySource: chilllabels.SourceNodeDiscovery,
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
	if _, ok := payload.Metadata.Labels[chilllabels.DeviceClass]; ok {
		t.Fatalf("device class label was included in node-discovery patch")
	}
	if payload.Metadata.Labels[chilllabels.DeviceModel] != "orin-nano" {
		t.Fatalf("device model label = %q, want orin-nano", payload.Metadata.Labels[chilllabels.DeviceModel])
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
	if node.Annotations[chilllabels.NodeDiscoveryResult] != chilllabels.DiscoveryResultUnmatched {
		t.Fatalf("node discovery result = %q, want %q", node.Annotations[chilllabels.NodeDiscoveryResult], chilllabels.DiscoveryResultUnmatched)
	}
	if node.Annotations[chilllabels.NodeDiscoveryReason] != chilllabels.DiscoveryReasonNoSourceFacts {
		t.Fatalf("node discovery reason = %q, want %q", node.Annotations[chilllabels.NodeDiscoveryReason], chilllabels.DiscoveryReasonNoSourceFacts)
	}
	if _, ok := node.Labels[chilllabels.DeviceClass]; ok {
		t.Fatalf("node-discovery set DeviceClass label")
	}
}
