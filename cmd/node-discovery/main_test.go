package main

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
