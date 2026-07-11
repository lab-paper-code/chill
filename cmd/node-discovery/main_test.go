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

	if err := runOnce(ctx, clientset, "edge-01", "chill", t.TempDir(), signatureFile); err != nil {
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
	if node.Annotations[metadata.System] != "chill" {
		t.Fatalf("system annotation = %q, want chill", node.Annotations[metadata.System])
	}
}
