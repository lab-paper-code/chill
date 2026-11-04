package kube

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// powerWriter is the power-mode writer of Operations on a store that holds
// node; beforePatch runs before each Node patch and afterPatch after it.
func powerWriter(t *testing.T, node *core.Node, measurement bool, beforePatch, afterPatch func(context.Context, client.WithWatch)) (*servingops.Operations, *int) {
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
	return &servingops.Operations{Store: &Store{client: c}, Config: servingops.OperationConfig{PollInterval: time.Millisecond, Measurement: measurement}}, &patches
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
// switch on an untainted Node, a measurement switch on a Node whose taint
// names its own root.
func TestPowerModeWriterPatchesOnlyAnAdmittedNode(t *testing.T) {
	_, _, _, class := observerSourceFixture()
	class.Spec.PowerModes = []edge.PowerMode{{Name: lowMode, Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 1}}}}
	settle := settleAgent(&class)
	for _, test := range []struct {
		name                 string
		measurement, tainted bool
		holder, uid          string
		patched              bool
	}{
		{name: "planner on untainted Node", uid: "node-uid", patched: true},
		{name: "planner on measured Node", tainted: true, uid: "node-uid"},
		{name: "measurement on untainted Node", measurement: true, holder: "root-uid", uid: "node-uid"},
		{name: "measurement on its own measured Node", measurement: true, tainted: true, holder: "root-uid", uid: "node-uid", patched: true},
		{name: "measurement on another root's Node", measurement: true, tainted: true, holder: "other-root-uid", uid: "node-uid"},
		{name: "replaced Node", uid: "old-uid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, node, _, _ := observerSourceFixture()
			if test.tainted {
				node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "root-uid", Effect: core.TaintEffectNoSchedule}}
			}
			operations, patches := powerWriter(t, node, test.measurement, nil, func(ctx context.Context, c client.WithWatch) { changeNode(ctx, c, settle) })
			err := operations.RequestPowerMode(context.Background(), serving.NodeReference{Name: "node", UID: test.uid}, test.holder, lowMode, time.Minute)
			if (err == nil) != test.patched || (*patches == 1) != test.patched {
				t.Fatalf("err=%v patches=%d", err, *patches)
			}
		})
	}
}

// The writer fails a mode the agent did not settle within its policy duration
// of the write, and a mode the agent failed.
func TestPowerModeWriterFailsPastDeadlineOrFailure(t *testing.T) {
	_, node, _, _ := observerSourceFixture()
	operations, _ := powerWriter(t, node, false, nil, nil)
	if err := operations.RequestPowerMode(context.Background(), testNode, "", lowMode, 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not reach power mode eco") {
		t.Fatalf("past deadline: %v", err)
	}
	_, node, _, _ = observerSourceFixture()
	operations, _ = powerWriter(t, node, false, nil, func(ctx context.Context, c client.WithWatch) {
		changeNode(ctx, c, func(n *core.Node) {
			chillmeta.SetPowerModeTransition(n, chillmeta.PowerModeFailed, 0, chillmeta.PowerModeTransitionRecord{
				PowerMode: lowMode, RequestedAt: n.Annotations[chillmeta.PowerModeRequestedAt], Failure: "nvpmodel exited 1"})
		})
	})
	if err := operations.RequestPowerMode(context.Background(), testNode, "", lowMode, time.Minute); err == nil || !strings.Contains(err.Error(), "nvpmodel exited 1") {
		t.Fatalf("failed apply: %v", err)
	}
}

// Each record expects the generation to advance by the power changes it made
// itself; any other advance is PowerChangedDuringMeasurement.
func TestMeasurementIsolationJudgesPowerModeGeneration(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation servingstate.Operation
		current   string
		start     *servingops.PowerModeGeneration
		end       int64
		changed   bool
	}{
		{name: "power into another mode", operation: servingstate.Operation{Kind: servingstate.OperationPower, PowerMode: maxMode}, current: "low", start: &servingops.PowerModeGeneration{Start: 3}, end: 4},
		{name: "power into the same mode", operation: servingstate.Operation{Kind: servingstate.OperationPower, PowerMode: maxMode}, current: maxMode, start: &servingops.PowerModeGeneration{Start: 3}, end: 3},
		{name: "power changed twice", operation: servingstate.Operation{Kind: servingstate.OperationPower, PowerMode: maxMode}, current: "low", start: &servingops.PowerModeGeneration{Start: 3}, end: 5, changed: true},
		{name: "prepare", operation: servingstate.Operation{Kind: servingstate.OperationPrepare}, start: &servingops.PowerModeGeneration{Start: 3}, end: 3},
		{name: "prepare changed", operation: servingstate.Operation{Kind: servingstate.OperationPrepare}, start: &servingops.PowerModeGeneration{Start: 3}, end: 4, changed: true},
		{name: "idle changed", operation: servingstate.Operation{Kind: servingstate.OperationIdle}, start: &servingops.PowerModeGeneration{Start: 3}, end: 4, changed: true},
		{name: "remove changed", operation: servingstate.Operation{Kind: servingstate.OperationRemove}, start: &servingops.PowerModeGeneration{Start: 3}, end: 4, changed: true},
		{name: "occupancy load window changed", operation: servingstate.Operation{Kind: occupancyKind}, start: &servingops.PowerModeGeneration{Start: 3}, end: 4, changed: true},
		{name: "Fixed Node", operation: servingstate.Operation{Kind: servingstate.OperationPrepare}, end: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, node, _, _ := observerSourceFixture()
			node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "root-uid", Effect: core.TaintEffectNoSchedule}}
			node.Annotations[chillmeta.PowerModeGeneration] = strconv.FormatInt(test.end, 10)
			scheme := runtime.NewScheme()
			if err := edge.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			operations := &servingops.Operations{Kube: kubefake.NewSimpleClientset(node), Store: &Store{client: fake.NewClientBuilder().WithScheme(scheme).Build()}}
			changes := expectedPowerModeChanges(test.operation, test.current)
			witness, err := operations.MeasurementIsolation(context.Background(), testNode, nil, test.start, changes)
			if changed := err != nil && strings.HasPrefix(err.Error(), servingstate.PowerChangedReason); changed != test.changed || !changed && err != nil {
				t.Fatalf("changes=%d err=%v", changes, err)
			}
			if (witness.PowerModeGeneration == nil) != (test.start == nil) || test.start != nil && *witness.PowerModeGeneration != (servingops.PowerModeGeneration{Start: test.start.Start, End: test.end}) {
				t.Fatalf("generation %+v", witness.PowerModeGeneration)
			}
			// An operation record keeps the witness and loses its cost.
			joules := 1.0
			record := servingops.OperationMeasurement{Verdict: servingstate.MeasurementAccepted, WallJoules: &joules, Request: servingops.MeasurementRequest{Operation: servingstate.Operation{Node: testNode}}}
			if applied := record.ApplyIsolation(witness, err); !errors.Is(applied, err) || test.changed != (record.Verdict == servingops.MeasurementInsufficient && strings.Contains(record.Reason, servingstate.PowerChangedReason) && record.WallJoules == nil) {
				t.Fatalf("record %+v", record)
			}
		})
	}
}

func TestPowerRebootOutcomeRecordsDirectedFact(t *testing.T) {
	const failedReboot = "failed-reboot"
	const rebootScenario = "reboot"
	const inPlace = "in-place"
	for _, scenario := range []string{rebootScenario, inPlace, "lost-without-reboot", failedReboot} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, snapshot, outstanding, _ := pendingPowerController(t, ctx)
			outstanding.Store(false)
			ref := snapshot.Nodes[0].Node
			for _, instance := range snapshot.Instances {
				if err := c.operations.StopInstance(ctx, instance.ID); err != nil {
					t.Fatal(err)
				}
			}
			api := c.store.client.(*nodeAPI)
			node, err := api.kube.CoreV1().Nodes().Get(ctx, ref.Name, meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			node.Status.NodeInfo.BootID = "before"
			if _, err = api.kube.CoreV1().Nodes().Update(ctx, node, meta.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			agent := api.agent
			api.agent = func(node *core.Node) {
				agent(node)
				if scenario == rebootScenario || scenario == failedReboot {
					node.Status.NodeInfo.BootID = "after"
				}
				if scenario == failedReboot {
					chillmeta.SetPowerModeTransition(node, chillmeta.PowerModeFailed, 1, chillmeta.PowerModeTransitionRecord{
						PowerMode: lowMode, RequestedAt: node.Annotations[chillmeta.PowerModeRequestedAt], Failure: "actuator failed"})
				}
				if scenario != inPlace {
					pods, e := api.kube.CoreV1().Pods("chill-system").List(ctx, meta.ListOptions{})
					if e != nil {
						t.Fatal(e)
					}
					for _, pod := range pods.Items {
						pod.Status.Phase = core.PodFailed
						for index := range pod.Status.ContainerStatuses {
							pod.Status.ContainerStatuses[index].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ExitCode: 255, Reason: "Unknown"}}
						}
						if _, e = api.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, &pod, meta.UpdateOptions{}); e != nil {
							t.Fatal(e)
						}
					}
				}
			}
			result, err := c.operations.Execute(ctx, servingstate.Operation{ID: "power", Kind: servingstate.OperationPower, Node: ref, PowerMode: lowMode, MaxActuationDuration: time.Second})
			wantSuccess := scenario == rebootScenario || scenario == inPlace
			if (err == nil) != wantSuccess || wantSuccess && result.Status != servingstate.NativeOperationComplete {
				t.Fatalf("mode outcome: status=%s err=%v", result.Status, err)
			}
			facts := c.observer.powerRebootFacts()[ref]
			reboot := scenario == rebootScenario || scenario == failedReboot
			if reboot && (len(facts) != 1 || facts[0].From != snapshot.Nodes[0].PowerMode || facts[0].To != lowMode || facts[0].BeforeBootID != "before" || facts[0].AfterBootID != "after") || !reboot && len(facts) != 0 {
				t.Fatalf("observed directed facts: %+v", facts)
			}
			if facts := c.observer.powerRebootFacts()[serving.NodeReference{Name: ref.Name, UID: "replacement"}]; len(facts) != 0 {
				t.Fatalf("reboot fact leaked to replacement node: %+v", facts)
			}
			next, err := c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			if len(next.Nodes[0].Reboots) != len(facts) {
				t.Fatalf("next planner snapshot lost observed facts: %+v", next.Nodes)
			}
		})
	}
}

func TestObserverRefreshRetiresAbsentNodeRebootFacts(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(strconv.FormatBool(replaced), func(t *testing.T) {
			ctx := context.Background()
			observer := newTestObserver(t)
			_, node, _, _ := observerSourceFixture()
			keep := node.DeepCopy()
			keep.Name, keep.UID = "keep", "keep-uid"
			for _, current := range []*core.Node{node, keep} {
				if _, err := observer.kube.CoreV1().Nodes().Create(ctx, current, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
				observer.RecordPowerReboot(serving.NodeReference{Name: current.Name, UID: string(current.UID)}, serving.PowerReboot{From: "before", To: "after"})
			}
			if err := observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			if len(observer.powerRebootFacts()) != 2 {
				t.Fatal("refresh discarded a current Node's facts")
			}
			if err := observer.kube.CoreV1().Nodes().Delete(ctx, node.Name, meta.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			if replaced {
				node.UID += replacementSuffix
				if _, err := observer.kube.CoreV1().Nodes().Create(ctx, node, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			facts := observer.powerRebootFacts()
			if len(facts) != 1 || len(facts[serving.NodeReference{Name: keep.Name, UID: string(keep.UID)}]) != 1 {
				t.Fatalf("refresh retained an absent Node UID or lost current facts: %+v", facts)
			}
		})
	}
}
