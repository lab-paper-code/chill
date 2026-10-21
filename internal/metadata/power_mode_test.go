package metadata

import (
	"context"
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

func powerModeNode(state string, generation int64, record PowerModeTransitionRecord) *corev1.Node {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetson"}}
	SetPowerModeTransition(node, state, generation, record)
	return node
}

func stamp(at time.Time) string { return at.Format(time.RFC3339Nano) }

// A stale read conflicts; a failed mode gets a new request time in the same
// patch as the label, which the agent serves as a new request.
func TestRequestPowerModeConflictsOnStaleReadAndRetriesFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	first := time.Unix(1_000, 0).UTC()
	failed := powerModeNode(PowerModeFailed, 0, PowerModeTransitionRecord{PowerMode: "7W", RequestedAt: stamp(first), Failure: "refused"})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(failed).Build()
	ctx := context.Background()
	stale := &corev1.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "jetson"}, stale); err != nil {
		t.Fatal(err)
	}
	fresh := stale.DeepCopy()
	at := time.Unix(2_000, 0).UTC()
	if err := RequestPowerMode(ctx, c, fresh, "7W", at); err != nil {
		t.Fatal(err)
	}
	if err := RequestPowerMode(ctx, c, stale, "15W", at); !apierrors.IsConflict(err) {
		t.Fatalf("stale write = %v, want conflict", err)
	}
	transition, _, _ := NodePowerModeTransition(fresh)
	if fresh.Labels[PowerMode] != "7W" || fresh.Annotations[PowerModeRequestedAt] != stamp(at) ||
		ServesRequest(fresh, transition, "7W") {
		t.Fatalf("retried Node = %v %v", fresh.Labels, fresh.Annotations)
	}
	again := fresh.DeepCopy()
	if err := RequestPowerMode(ctx, c, again, "7W", at.Add(time.Hour)); err != nil || again.ResourceVersion != fresh.ResourceVersion {
		t.Fatalf("a pending request was written again: %v", err)
	}
}

// The writer's deadline counts from its own request time on the Node, so a
// Node whose agent never starts the apply still fails in time.
func TestPowerModeOutcome(t *testing.T) {
	start := time.Unix(1_000, 0).UTC()
	ready := start.Add(time.Minute)
	recent := start.Add(50 * time.Minute)
	record := func(at time.Time) PowerModeTransitionRecord {
		return PowerModeTransitionRecord{PowerMode: "7W", RequestedAt: stamp(at), Generation: 3}
	}
	settled := record(start)
	settled.ApplyStartedAt, settled.ReadyAt = &start, &ready
	failure := record(start)
	failure.Failure = "refused"
	fencedLate := record(start)
	fencedLate.ApplyStartedAt = &recent
	previous := record(recent)
	previous.PowerMode, previous.ReadyAt = "15W", &ready
	for name, test := range map[string]struct {
		node *corev1.Node
		done bool
		err  string
	}{
		"settled":                              {node: powerModeNode(PowerModeSuccess, 3, settled), done: true},
		"changed since":                        {node: powerModeNode(PowerModeSuccess, 4, settled), err: "left power mode"},
		"failed":                               {node: powerModeNode(PowerModeFailed, 3, failure), err: "refused"},
		"pending past the request time":        {node: powerModeNode(PowerModePending, 3, fencedLate), err: "within"},
		"never fenced past the request time":   {node: powerModeNode("", 3, record(start)), err: "within"},
		"never fenced within the request time": {node: powerModeNode("", 3, record(recent))},
		"previous mode's success":              {node: powerModeNode(PowerModeSuccess, 3, previous)},
		"no request time": {node: func() *corev1.Node {
			node := powerModeNode("", 3, record(start))
			delete(node.Annotations, PowerModeRequestedAt)
			return node
		}(), err: "request time"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "previous mode's success" {
				test.node.Labels[PowerMode] = "7W"
			}
			done, err := PowerModeOutcome(test.node, "7W", 15*time.Minute, start.Add(time.Hour))
			if done != test.done || (err == nil) != (test.err == "") || (err != nil && !strings.Contains(err.Error(), test.err)) {
				t.Fatalf("outcome = %v, %v", done, err)
			}
		})
	}
}

// A success the host has left since (a hand change, a reboot) is requested
// again with a new request time, so a writer can take the Node back to mode.
func TestRequestPowerModeRetriesASuccessTheHostLeft(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	first := time.Unix(1_000, 0).UTC()
	ready := first.Add(time.Minute)
	left := powerModeNode(PowerModeSuccess, 4, PowerModeTransitionRecord{PowerMode: "7W", RequestedAt: stamp(first), Generation: 3, ReadyAt: &ready})
	held := powerModeNode(PowerModeSuccess, 3, PowerModeTransitionRecord{PowerMode: "7W", RequestedAt: stamp(first), Generation: 3, ReadyAt: &ready})
	held.Name = "held"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(left, held).Build()
	ctx := context.Background()
	at := time.Unix(2_000, 0).UTC()
	for _, test := range []struct {
		name, want string
	}{{name: "jetson", want: stamp(at)}, {name: "held", want: stamp(first)}} {
		node := &corev1.Node{}
		if err := c.Get(ctx, client.ObjectKey{Name: test.name}, node); err != nil {
			t.Fatal(err)
		}
		if err := RequestPowerMode(ctx, c, node, "7W", at); err != nil {
			t.Fatal(err)
		}
		if got := node.Annotations[PowerModeRequestedAt]; got != test.want {
			t.Fatalf("%s request time = %s, want %s", test.name, got, test.want)
		}
	}
}
