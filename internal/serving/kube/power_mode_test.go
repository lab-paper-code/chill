package kube

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// powerWriter is the power-mode writer of Operations on a store that holds
// node; beforePatch runs before each Node patch and afterPatch after it.
func powerWriter(t *testing.T, node *core.Node, measurement bool, beforePatch, afterPatch func(context.Context, client.WithWatch)) (*Operations, *int) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	patches := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches++
			if beforePatch != nil {
				beforePatch(ctx, c)
			}
			if err := c.Patch(ctx, obj, patch, opts...); err != nil {
				return err
			}
			if afterPatch != nil {
				afterPatch(ctx, c)
			}
			return nil
		},
	}).Build()
	return &Operations{store: &Store{client: c}, config: OperationConfig{PollInterval: time.Millisecond, Measurement: measurement}}, &patches
}

// changeNode applies change to the stored Node.
func changeNode(ctx context.Context, c client.WithWatch, change func(*core.Node)) {
	node := &core.Node{}
	if err := c.Get(ctx, client.ObjectKey{Name: "node"}, node); err != nil {
		panic(err)
	}
	change(node)
	if err := c.Update(ctx, node); err != nil {
		panic(err)
	}
}

// Only the writer the Node read admits patches the desired mode: a planner
// switch on an untainted Node, a measurement switch on a tainted one.
func TestPowerModeWriterPatchesOnlyAnAdmittedNode(t *testing.T) {
	_, _, _, class := observerSourceFixture()
	class.Spec.PowerModes = []edge.PowerMode{{Name: lowMode, Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 1}}}}
	settle := settleAgent(&class)
	for _, test := range []struct {
		name                 string
		measurement, tainted bool
		uid                  string
		patched              bool
	}{
		{name: "planner on untainted Node", uid: "node-uid", patched: true},
		{name: "planner on measured Node", tainted: true, uid: "node-uid"},
		{name: "measurement on untainted Node", measurement: true, uid: "node-uid"},
		{name: "measurement on measured Node", measurement: true, tainted: true, uid: "node-uid", patched: true},
		{name: "replaced Node", uid: "old-uid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, node, _, _ := observerSourceFixture()
			if test.tainted {
				node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "root-uid", Effect: core.TaintEffectNoSchedule}}
			}
			operations, patches := powerWriter(t, node, test.measurement, nil, func(ctx context.Context, c client.WithWatch) { changeNode(ctx, c, settle) })
			err := operations.requestPowerMode(context.Background(), serving.NodeReference{Name: "node", UID: test.uid}, lowMode, time.Minute)
			if (err == nil) != test.patched || (*patches == 1) != test.patched {
				t.Fatalf("err=%v patches=%d", err, *patches)
			}
		})
	}
}

// A Node that changed after the writer read it, and with it possibly its
// grant, rejects the write.
func TestPowerModeWriterConflictsOnStaleNode(t *testing.T) {
	_, node, _, _ := observerSourceFixture()
	operations, _ := powerWriter(t, node, false, func(ctx context.Context, c client.WithWatch) {
		changeNode(ctx, c, func(n *core.Node) {
			n.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "root-uid", Effect: core.TaintEffectNoSchedule}}
		})
	}, nil)
	if err := operations.requestPowerMode(context.Background(), testNode, lowMode, time.Minute); !apierrors.IsConflict(err) {
		t.Fatalf("stale Node write: %v", err)
	}
}

// The writer fails a mode the agent did not settle within its policy duration
// of the write, and a mode the agent failed.
func TestPowerModeWriterFailsPastDeadlineOrFailure(t *testing.T) {
	_, node, _, _ := observerSourceFixture()
	operations, _ := powerWriter(t, node, false, nil, nil)
	if err := operations.requestPowerMode(context.Background(), testNode, lowMode, 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not reach power mode eco") {
		t.Fatalf("past deadline: %v", err)
	}
	_, node, _, _ = observerSourceFixture()
	operations, _ = powerWriter(t, node, false, nil, func(ctx context.Context, c client.WithWatch) {
		changeNode(ctx, c, func(n *core.Node) {
			n.Labels[chillmeta.PowerModeState] = chillmeta.PowerModeFailed
			n.Annotations[chillmeta.PowerModeTransition] = `{"powerMode":"eco","generation":0,"failure":"nvpmodel exited 1"}`
		})
	})
	if err := operations.requestPowerMode(context.Background(), testNode, lowMode, time.Minute); err == nil || !strings.Contains(err.Error(), "nvpmodel exited 1") {
		t.Fatalf("failed apply: %v", err)
	}
}

// Each record expects the generation to advance by the power changes it made
// itself; any other advance is PowerChangedDuringMeasurement.
func TestMeasurementIsolationJudgesPowerModeGeneration(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation Operation
		current   string
		start     *PowerModeGeneration
		end       int64
		changed   bool
	}{
		{name: "power into another mode", operation: Operation{Kind: operationPower, PowerMode: maxMode}, current: "low", start: &PowerModeGeneration{Start: 3}, end: 4},
		{name: "power into the same mode", operation: Operation{Kind: operationPower, PowerMode: maxMode}, current: maxMode, start: &PowerModeGeneration{Start: 3}, end: 3},
		{name: "power changed twice", operation: Operation{Kind: operationPower, PowerMode: maxMode}, current: "low", start: &PowerModeGeneration{Start: 3}, end: 5, changed: true},
		{name: "prepare", operation: Operation{Kind: operationPrepare}, start: &PowerModeGeneration{Start: 3}, end: 3},
		{name: "prepare changed", operation: Operation{Kind: operationPrepare}, start: &PowerModeGeneration{Start: 3}, end: 4, changed: true},
		{name: "idle changed", operation: Operation{Kind: operationIdle}, start: &PowerModeGeneration{Start: 3}, end: 4, changed: true},
		{name: "remove changed", operation: Operation{Kind: operationRemove}, start: &PowerModeGeneration{Start: 3}, end: 4, changed: true},
		{name: "occupancy load window changed", operation: Operation{Kind: occupancyKind}, start: &PowerModeGeneration{Start: 3}, end: 4, changed: true},
		{name: "Fixed Node", operation: Operation{Kind: operationPrepare}, end: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, node, _, _ := observerSourceFixture()
			node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "root-uid", Effect: core.TaintEffectNoSchedule}}
			node.Annotations[chillmeta.PowerModeGeneration] = strconv.FormatInt(test.end, 10)
			scheme := runtime.NewScheme()
			if err := edge.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			operations := &Operations{kube: kubefake.NewSimpleClientset(node), store: &Store{client: fake.NewClientBuilder().WithScheme(scheme).Build()}}
			changes := expectedPowerModeChanges(test.operation, test.current)
			witness, err := operations.measurementIsolation(context.Background(), testNode, nil, test.start, changes)
			if changed := err != nil && strings.HasPrefix(err.Error(), powerChangedReason); changed != test.changed || !changed && err != nil {
				t.Fatalf("changes=%d err=%v", changes, err)
			}
			if (witness.PowerModeGeneration == nil) != (test.start == nil) || test.start != nil && *witness.PowerModeGeneration != (PowerModeGeneration{Start: test.start.Start, End: test.end}) {
				t.Fatalf("generation %+v", witness.PowerModeGeneration)
			}
			// An operation record keeps the witness and loses its cost.
			joules := 1.0
			record := OperationMeasurement{Verdict: measurementAccepted, WallJoules: &joules, Request: MeasurementRequest{Operation: Operation{Node: testNode}}}
			if applied := record.applyIsolation(witness, err); !errors.Is(applied, err) || test.changed != (record.Verdict == measurementInsufficient && strings.Contains(record.Reason, powerChangedReason) && record.WallJoules == nil) {
				t.Fatalf("record %+v", record)
			}
		})
	}
}
