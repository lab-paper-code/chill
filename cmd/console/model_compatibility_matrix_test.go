package main

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCompatibilityMemoReadsEachUniqueObjectOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid", ResourceVersion: "1"}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "system", Name: "facts", UID: "facts-uid", ResourceVersion: "2"}, Data: map[string]string{"facts": "source"}}
	reader := &countingCompatibilityReader{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, config).Build(), gets: make(map[string]int)}
	memo := &compatibilityMemoReader{Reader: reader, objects: make(map[compatibilityMemoKey]compatibilityMemoValue)}
	ctx := context.Background()
	if err := memo.List(ctx, &corev1.NodeList{}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		gotNode := &corev1.Node{}
		if err := memo.Get(ctx, client.ObjectKey{Name: "node"}, gotNode); err != nil || gotNode.UID != node.UID {
			t.Fatalf("seeded Node = %+v, %v", gotNode, err)
		}
		gotConfig := &corev1.ConfigMap{}
		if err := memo.Get(ctx, client.ObjectKey{Namespace: "system", Name: "facts"}, gotConfig); err != nil || gotConfig.Data["facts"] != "source" {
			t.Fatalf("memo facts = %+v, %v", gotConfig, err)
		}
	}
	if len(reader.gets) != 1 || reader.gets["*v1.ConfigMap:system/facts"] != 1 {
		t.Fatalf("duplicate GETs: %+v", reader.gets)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := memo.Get(ctx, client.ObjectKey{Name: "node"}, &corev1.Node{}); err != context.Canceled {
		t.Fatalf("memo hid cancellation: %v", err)
	}
}
