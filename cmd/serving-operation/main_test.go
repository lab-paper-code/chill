package main

import (
	"context"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMeasurementMembershipIsolation(t *testing.T) {
	const isolated = "measurement-campaign"
	cases := []struct {
		name, kind, source, target, uid string
		allowed                         bool
	}{
		{"join detached node", "membership", "", isolated, "node-uid", true},
		{"detach isolated node", "membership", isolated, "", "node-uid", true},
		{"reject detached no-op", "membership", "", "", "node-uid", false},
		{"reject attached no-op", "membership", isolated, isolated, "node-uid", false},
		{"idle isolated node", "idle", isolated, "", "node-uid", true},
		{"reject detached idle", "idle", "", "", "node-uid", false},
		{"preserve another source pool", "membership", "live-pool", isolated, "node-uid", false},
		{"preserve another measurement pool", "membership", "measurement-other", isolated, "node-uid", false},
		{"reject another target pool", "membership", isolated, "measurement-other", "node-uid", false},
		{"reject replaced physical node", "membership", "", isolated, "old-uid", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kube := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name: "node", UID: "node-uid", Labels: map[string]string{servingkube.ServingPoolLabel: tc.source},
			}})
			manifest := commandManifest{IsolatedPool: isolated}
			manifest.Measurement.Operation = servingkube.Operation{
				Kind: tc.kind, Node: serving.NodeReference{Name: "node", UID: tc.uid}, Pool: tc.target,
			}
			pool, err := validateMeasurementNode(context.Background(), kube, manifest)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t, error=%v", tc.allowed, err)
			}
			if tc.allowed && pool != tc.source {
				t.Fatalf("initial isolation pool=%q, actual source=%q", pool, tc.source)
			}
		})
	}
}
