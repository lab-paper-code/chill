package metadata

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func powerModeNode(labels map[string]string, generation string, transition PowerModeTransitionRecord) *corev1.Node {
	raw, _ := json.Marshal(transition)
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetson", Labels: labels, Annotations: map[string]string{
		PowerModeGeneration: generation, PowerModeTransition: string(raw),
	}}}
}

func TestRequestPowerModeConflictsOnStaleReadAndRetriesFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	failed := powerModeNode(map[string]string{PowerMode: "7W", PowerModeState: PowerModeFailed}, "0",
		PowerModeTransitionRecord{PowerMode: "7W", Failure: "refused"})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(failed).Build()
	ctx := context.Background()
	stale := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "jetson"}, stale); err != nil {
		t.Fatal(err)
	}
	fresh := stale.DeepCopy()
	if err := RequestPowerMode(ctx, c, fresh, "7W"); err != nil {
		t.Fatal(err)
	}
	if err := RequestPowerMode(ctx, c, stale, "15W"); !apierrors.IsConflict(err) {
		t.Fatalf("stale write = %v, want conflict", err)
	}
	if fresh.Labels[PowerMode] != "7W" || fresh.ResourceVersion == stale.ResourceVersion {
		t.Fatalf("retried Node = %v rv %s", fresh.Labels, fresh.ResourceVersion)
	}
}

func TestPowerModeOutcome(t *testing.T) {
	start := time.Unix(1_000, 0).UTC()
	ready := start.Add(time.Minute)
	for name, test := range map[string]struct {
		node  *corev1.Node
		since time.Time
		done  bool
		err   string
	}{
		"settled": {node: powerModeNode(map[string]string{PowerMode: "7W", PowerModeState: PowerModeSuccess}, "3",
			PowerModeTransitionRecord{PowerMode: "7W", Generation: 3, ApplyStartedAt: &start, ReadyAt: &ready}), done: true},
		"changed since": {node: powerModeNode(map[string]string{PowerMode: "7W", PowerModeState: PowerModeSuccess}, "4",
			PowerModeTransitionRecord{PowerMode: "7W", Generation: 3, ReadyAt: &ready}), err: "left power mode"},
		"previous mode's success": {node: powerModeNode(map[string]string{PowerMode: "7W", PowerModeState: PowerModeSuccess}, "3",
			PowerModeTransitionRecord{PowerMode: "15W", Generation: 3, ReadyAt: &ready}), since: start.Add(50 * time.Minute)},
		"failed": {node: powerModeNode(map[string]string{PowerMode: "7W", PowerModeState: PowerModeFailed}, "3",
			PowerModeTransitionRecord{PowerMode: "7W", Failure: "refused"}), err: "refused"},
		"pending past the fence deadline": {node: powerModeNode(map[string]string{PowerMode: "7W", PowerModeState: PowerModePending}, "3",
			PowerModeTransitionRecord{PowerMode: "7W", Generation: 3, ApplyStartedAt: &start}), err: "within"},
		"unfenced past the request deadline": {node: powerModeNode(map[string]string{PowerMode: "7W"}, "3",
			PowerModeTransitionRecord{}), since: start, err: "within"},
		"unfenced without a request time": {node: powerModeNode(map[string]string{PowerMode: "7W"}, "3",
			PowerModeTransitionRecord{})},
	} {
		t.Run(name, func(t *testing.T) {
			done, err := PowerModeOutcome(test.node, "7W", test.since, 15*time.Minute, start.Add(time.Hour))
			if done != test.done || (err == nil) != (test.err == "") || (err != nil && !strings.Contains(err.Error(), test.err)) {
				t.Fatalf("outcome = %v, %v", done, err)
			}
		})
	}
}
