package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// The API boundary supplies Pod creation/termination, not operation receipts.
func handoffController(t *testing.T) *SchedulerController {
	t.Helper()
	c, ready := tickReadySubsetController(t, nil, "c", "d")
	ready.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	c.ctx = ctx
	t.Cleanup(func() { cancel(); c.wg.Wait() })
	old, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "b", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.kube.CoreV1().Pods(old.Namespace).Delete(ctx, old.Name, meta.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.store.client.Delete(ctx, old); err != nil {
		t.Fatal(err)
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes.Items {
		if err = c.store.client.Create(ctx, node.DeepCopy()); err != nil {
			t.Fatal(err)
		}
	}
	nativeClient := interceptor.NewClient(c.store.client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			pod, ok := obj.(*core.Pod)
			if !ok {
				return cl.Create(ctx, obj, opts...)
			}
			pod.UID = types.UID(pod.Name + "-pod")
			pod.Labels[servingstate.InstancePodUIDLabel] = string(pod.UID)
			pod.Status = old.Status
			if err := cl.Create(ctx, pod, opts...); err != nil {
				return err
			}
			if err := c.kube.(*kubefake.Clientset).Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace); err != nil {
				return err
			}
			follow, stop := context.WithCancel(c.observer.ctx)
			c.observer.mu.Lock()
			c.observer.sources[string(pod.UID)] = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: servingstate.FirstfloorStatus(pod).ContainerID, follow: follow, cancel: stop}
			c.observer.mu.Unlock()
			return nil
		},
	})
	c.store.client = &resetAPI{Client: nativeClient, kube: c.kube.(*kubefake.Clientset), terminateOnDelete: true}
	var serviceSequence atomic.Int32
	c.kube.(*kubefake.Clientset).PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*core.Service).Spec.ClusterIP = fmt.Sprintf("10.96.1.%d", serviceSequence.Add(1)+10)
		return false, nil, nil
	})
	settings := &c.config.Settings
	settings.Controls = serving.SchedulerControls{Placement: true, Resources: true, Membership: true, Replicas: true, Routing: true, Batching: true}
	settings.MaxInstances, settings.MaxReplicas = 4, 4
	settings.ScaleDownStabilization = time.Hour
	settings.CapacityWindow = 10 * time.Millisecond
	// Deterministic measured durations need one tail sample for this lifecycle
	// fixture; population-heavy statistical precision is tested in serving.
	settings.CapacityTailSamples = 1
	settings.ObservationWindow = 100 * time.Millisecond
	settings.ControlInterval = time.Millisecond
	uid := settings.AutonomousWorkloads[0]
	settings.MinimumDemand = map[string]float64{uid: 0}
	c.config.Preparations = map[string]servingstate.PrepareSpec{}
	for _, profile := range c.observer.Profiles() {
		spec := measuredCPUPreparationFixture()
		spec.Instance = serving.SchedulerInstance{ID: "replacement", Node: serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}, Profile: &profile, Allocation: serving.SchedulerAllocation{MemoryBytes: 1024}, MaxBatch: 1, Capacity: 4, WorkloadUID: uid}
		spec.Config.Policy.ProfileID = profile.ID
		c.config.Preparations[profile.ID] = spec
		c.config.DispatchOccupancy = append(c.config.DispatchOccupancy, serving.DispatchOccupancy{Node: spec.Instance.Node, PowerMode: profile.Condition.PowerMode, RuntimeImage: profile.Condition.RuntimeImage, Model: profile.Condition.LogicalModel, Source: "measured handoff fixture"})
		ref := evidencestore.ObjectIdentity{Store: "raw", Key: "baseline-" + profile.ID, Size: 1}
		c.config.Baselines = append(c.config.Baselines, serving.NodeBaseline{ID: ref.Key, Node: spec.Instance.Node, PowerMode: "fixed", Watts: 10, Meter: "wall-" + profile.NodeName, Scope: "node-exclusive-wall", Reference: ref, MeasuredAt: time.Now()})
		c.config.Costs = append(c.config.Costs, serving.OperationCost{ID: "prepare-" + profile.ID, Node: spec.Instance.Node, Kind: servingstate.OperationPrepare, From: "unloaded", To: serving.SchedulerInstanceState(spec.Instance), PowerMode: "fixed", Trials: []serving.OperationTrial{{Duration: time.Millisecond, Joules: 0.01}}, Meter: "wall-" + profile.NodeName, Scope: "node-exclusive-wall", EnergyScope: "whole-wall", Reference: ref})
	}
	snapshot := handoffSnapshot(t, c)
	c.runtime.EnableSchedulerRouting()
	c.capacity.Ensure(ctx, *settings, snapshot)
	c.capacity.Wait()
	return c
}

func handoffSnapshot(t *testing.T, c *SchedulerController) serving.SchedulerSnapshot {
	t.Helper()
	snapshot, err := c.captureReconciled(c.ctx, time.Now(), c.config, false)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.publishSnapshotLocked(&snapshot)
	c.mu.Unlock()
	return snapshot
}

func handoffDispatch(t *testing.T, c *SchedulerController, goal *serving.SchedulerCandidate, actions ...serving.Action) {
	t.Helper()
	selected := *goal
	selected.Actions = actions
	dispatch := func() { c.launchTargetLocked(selected, c.config) }
	if c.runs[transitionRunKind+"/"+goal.ID] == nil {
		c.startTransition(goal, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, dispatch)
	} else {
		c.mu.Lock()
		dispatch()
		c.mu.Unlock()
	}
	c.wg.Wait()
}

func admittedHandoff(t *testing.T, partial bool) (*SchedulerController, *serving.SchedulerCandidate) {
	t.Helper()
	c := handoffController(t)
	snapshot := handoffSnapshot(t, c)
	uid := snapshot.Workloads[0].UID
	byNode := map[string]serving.SchedulerInstance{}
	for _, instance := range snapshot.Instances {
		instance.WorkloadUID = uid
		byNode[instance.Node.Name] = instance
	}
	a, untouched, d := byNode["a"], byNode["c"], byNode["d"]
	replacement := c.config.Preparations["b-profile"].Instance
	goal := &serving.SchedulerCandidate{ID: "physical-owner", Instances: []serving.SchedulerInstance{replacement}, Routing: map[string]map[string]int32{uid: {replacement.ID: 1000}}, Actions: []serving.Action{
		{ID: "prepare-replacement", Kind: servingstate.OperationPrepare, Instance: replacement.ID, Node: replacement.Node},
		{ID: "final-handoff", Kind: servingstate.OperationRoute, DependsOn: []string{"prepare-replacement"}},
		{ID: "stop-source", Kind: servingstate.OperationStop, Instance: a.ID, Node: a.Node, DependsOn: []string{"final-handoff"}},
		{ID: "drain-source", Kind: servingstate.OperationDrain, Instance: a.ID, Node: a.Node, DependsOn: []string{"stop-source"}},
		{ID: "remove-source", Kind: servingstate.OperationRemove, Instance: a.ID, Node: a.Node, DependsOn: []string{"drain-source"}},
		{ID: "unstarted-stop", Kind: servingstate.OperationStop, Instance: untouched.ID, Node: untouched.Node, DependsOn: []string{"remove-source"}},
		{ID: "failed-route", Kind: servingstate.OperationRoute, DependsOn: []string{"stop-source"}},
	}}
	if partial {
		goal.Actions[1].RouteTarget = &serving.SchedulerCandidate{ID: "ready-source-subset", Instances: []serving.SchedulerInstance{a}, Routing: map[string]map[string]int32{uid: {a.ID: 1000}}}
		other := &serving.SchedulerCandidate{ID: "other-source-handoff", Instances: []serving.SchedulerInstance{a, untouched}, Routing: map[string]map[string]int32{uid: {a.ID: 1000, untouched.ID: 0}}}
		goal.Actions = append(goal.Actions, serving.Action{ID: "other-handoff", Kind: servingstate.OperationRoute, RouteTarget: other, DependsOn: []string{"prepare-replacement"}})
		goal.Actions[5].DependsOn = []string{"other-handoff"}
	}
	c.startTransition(goal, c.config.Settings.Controls, func() {}, func(serving.Action) bool { return true }, func() {})
	newer := &serving.SchedulerCandidate{ID: "newer-reweight", Instances: []serving.SchedulerInstance{a, d}, Routing: map[string]map[string]int32{uid: {a.ID: 900, d.ID: 100}}, Actions: []serving.Action{{ID: "newer-route", Kind: servingstate.OperationRoute}}}
	handoffDispatch(t, c, newer, newer.Actions[0])
	requireCompletedContinuationAction(t, c.runs["newer-route"], "newer route")
	handoffSnapshot(t, c)
	handoffDispatch(t, c, goal, goal.Actions[0])
	requireCompletedContinuationAction(t, c.runs["prepare-replacement"], "actual preparation")
	handoffSnapshot(t, c)
	handoffDispatch(t, c, goal, goal.Actions[1])
	requireCompletedContinuationAction(t, c.runs["final-handoff"], "actual handoff")
	handoffSnapshot(t, c)
	if partial {
		handoffDispatch(t, c, goal, goal.Actions[7])
		requireCompletedContinuationAction(t, c.runs["other-handoff"], "independent completed handoff")
		handoffSnapshot(t, c)
	}
	return c, goal
}

func TestBorrowedRouteContextCannotPreserveOwnersRetiree(t *testing.T) {
	c, goal := admittedHandoff(t, false)
	uid := c.config.Settings.AutonomousWorkloads[0]
	route := c.runs["final-handoff"].result.Operation.Target.Routing[uid]
	if route[goal.Actions[2].Instance] != 0 || route[c.runs["prepare-replacement"].prepareIdentity] <= 0 {
		t.Fatalf("borrowed writer displaced prepared replacement: %v", route)
	}
	handoffDispatch(t, c, goal, goal.Actions[2])
	requireCompletedContinuationAction(t, c.runs["stop-source"], "safe replacement handoff must allow stop")
	handoffSnapshot(t, c)
	if c.runs["final-handoff"].result.Status != servingstate.NativeOperationComplete {
		t.Fatal("normal handoff became completed effect lost after stop")
	}
}

func TestPositiveRebasedSourceRefusesStopAndReturnsKnownEffects(t *testing.T) {
	c, goal := admittedHandoff(t, true)
	handoffDispatch(t, c, goal, goal.Actions...)
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	if c.runs["stop-source"] != nil || c.runs["unstarted-stop"] != nil || parent.action.Status != servingstate.NativeOperationFailed {
		t.Fatal("positive rebased source was stopped or owner did not fail")
	}
	finishFailedHandoff(t, c, goal, false)
}

func TestFailedOwnerUnwindsStoppedSourceAndRestoresICAdmission(t *testing.T) {
	c, goal := admittedHandoff(t, false)
	handoffDispatch(t, c, goal, goal.Actions[2])
	requireCompletedContinuationAction(t, c.runs["stop-source"], "settled stop")
	c.config.OnAction = func(result servingstate.OperationResult) error {
		if result.Operation.ID == "failed-route" && result.Status == starting {
			return errors.New("persist route start refused")
		}
		return nil
	}
	handoffDispatch(t, c, goal, goal.Actions[6])
	c.config.OnAction = nil
	handoffSnapshot(t, c)
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	if parent.action.Status != servingstate.NativeOperationFailed || !parent.reserved || c.runs["drain-source"] != nil {
		t.Fatal("fixture did not retain a genuinely failed, physically frozen owner")
	}
	finishFailedHandoff(t, c, goal, true)
}

func finishFailedHandoff(t *testing.T, c *SchedulerController, goal *serving.SchedulerCandidate, stopped bool) {
	t.Helper()
	parent := c.runs[transitionRunKind+"/"+goal.ID]
	before, _ := c.runtime.SchedulerRouting()
	uid := c.config.Settings.AutonomousWorkloads[0]
	var receipts sync.Map
	c.config.OnAction = func(result servingstate.OperationResult) error {
		receipts.Store(result.Operation.ID+"/"+result.Status, result)
		return nil
	}
	var decision serving.SchedulerDecision
	for range 4 {
		var err error
		decision, err = c.Tick(c.ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		c.wg.Wait()
		if !parent.reserved {
			break
		}
	}
	if parent.reserved {
		t.Fatal("fresh untouched/prepare/absence proofs did not return the failed physical owner")
	}
	if _, started := receipts.Load("unstarted-stop/" + starting); started {
		t.Fatal("failed owner started a new stop instead of unwinding settled responsibility")
	}
	if stopped {
		for _, id := range []string{"drain-source", "remove-source"} {
			if _, complete := receipts.Load(id + "/" + servingstate.NativeOperationComplete); !complete {
				t.Fatalf("settled stop did not finish its normal cleanup: %s", id)
			}
		}
	}
	// Replanning returns held nodes to eligibility; actual IC admission must
	// reach previously unrouted Ready capacity without a UID/writer fence.
	if !slices.ContainsFunc(decision.Capacity.Plans, func(plan serving.SchedulerNodePlan) bool { return plan.Node.Name == "c" && plan.Eligible }) {
		t.Fatalf("released Ready node did not return to planner candidates: %v", decision.Reasons)
	}
	// Exercise a proposed Ready expansion through the normal admission boundary;
	// the candidate gate, not an optimizer choice, is the contract under test.
	admitReadyHandoffProposal(t, c)
	snapshot := handoffSnapshot(t, c)
	routing, _ := c.runtime.SchedulerRouting()
	if _, started := receipts.Load("post-return-ic-route/" + starting); !started {
		t.Fatal("no actual IC route was admitted after physical return")
	}
	if _, complete := receipts.Load("post-return-ic-route/" + servingstate.NativeOperationComplete); !complete {
		t.Fatal("admitted IC route did not complete native/runtime application")
	}
	if !slices.ContainsFunc(snapshot.Instances, func(instance serving.SchedulerInstance) bool {
		return slices.Contains(parent.scope.Nodes, instance.Node) && before[uid][instance.ID] <= 0 &&
			instance.Ready && !instance.Stopped && routing[uid][instance.ID] > 0
	}) {
		t.Fatalf("next IC route did not admit Ready capacity after return: %v; reasons=%v demand=%v capacity=%+v", routing, decision.Reasons, c.config.Settings.MinimumDemand, decision.Capacity.Workloads)
	}
}

func admitReadyHandoffProposal(t *testing.T, c *SchedulerController) {
	t.Helper()
	snapshot := handoffSnapshot(t, c)
	uid := c.config.Settings.AutonomousWorkloads[0]
	source := slices.IndexFunc(snapshot.Instances, func(instance serving.SchedulerInstance) bool {
		return instance.Node.Name == "c" && instance.Ready && !instance.Stopped
	})
	if source < 0 {
		t.Fatal("returned candidate is not Ready")
	}
	current, _ := c.runtime.SchedulerRouting()
	ready := map[string]bool{}
	for _, instance := range snapshot.Instances {
		ready[instance.ID] = instance.Ready && !instance.Stopped && !instance.Terminated
	}
	wanted := snapshot.Instances[source]
	weights := serving.SchedulerRebaseRouting(current[uid], map[string]int32{wanted.ID: 300}, ready, nil)
	next := serving.SchedulerCandidate{ID: "post-return-ic", Routing: map[string]map[string]int32{uid: weights},
		Actions: []serving.Action{{ID: "post-return-ic-route", Kind: servingstate.OperationRoute, Node: wanted.Node}}}
	for _, instance := range snapshot.Instances {
		if weights[instance.ID] > 0 {
			instance.WorkloadUID = uid
			next.Instances = append(next.Instances, instance)
		}
	}
	c.launch(next, c.config)
	c.wg.Wait()
}
