package kube

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// Block the real evaluator at its context boundary, without a controller hook.
type heldEvaluationContext struct {
	context.Context
	entered, release chan struct{}
	once             sync.Once
}

func (c *heldEvaluationContext) Err() error {
	pc, _, _, _ := runtime.Caller(1)
	if fn := runtime.FuncForPC(pc); fn != nil && strings.HasSuffix(fn.Name(), ".evaluateScheduler") {
		c.once.Do(func() {
			close(c.entered)
			<-c.release
		})
	}
	return c.Context.Err()
}

func holdSchedulerEvaluation(t *testing.T, c *SchedulerController) (<-chan error, func()) {
	t.Helper()
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ctx := &heldEvaluationContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	resume := func() { release.Do(func() { close(ctx.release) }) }
	done := make(chan error, 1)
	go func() { _, err := c.Tick(ctx, time.Now()); done <- err }()
	t.Cleanup(func() { resume(); cancel() })
	select {
	case <-ctx.entered:
	case err := <-done:
		t.Fatalf("Tick did not enter evaluation: %v", err)
	case <-parent.Done():
		t.Fatal(parent.Err())
	}
	return done, resume
}

func TestSchedulerEvaluationDoesNotBlockContinuationAndPublishesLatestCapture(t *testing.T) {
	c := schedulerEmptyPoolController(t)
	done, resume := holdSchedulerEvaluation(t, c)
	continued := make(chan error, 1)
	go func() { continued <- c.Continue(context.Background(), time.Now()) }()
	select {
	case err := <-continued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		resume()
		<-done
		<-continued
		t.Fatal("Continue waited for the running evaluation")
	}
	before := c.State().Snapshot
	resume()
	if err := <-done; err != nil {
		t.Fatalf("progress during evaluation discarded the Keep decision: %v", err)
	}
	if state := c.State(); !state.Snapshot.At.After(before.At) || state.Decision.At.IsZero() {
		t.Fatalf("Keep decision was not published on a capture after progress: %s -> %s", before.At, state.Snapshot.At)
	}
}

func TestSchedulerEvaluationRejectsChangedConfiguration(t *testing.T) {
	c := schedulerEmptyPoolController(t)
	done, resume := holdSchedulerEvaluation(t, c)
	settings := c.Settings()
	settings.Controls.Routing = false
	accepted := make(chan error, 1)
	go func() { accepted <- c.AcceptSettings(context.Background(), settings, nil) }()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		resume()
		<-done
		<-accepted
		t.Fatal("settings acceptance waited for evaluation")
	}
	resume()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "evaluation invalidated: configuration changed") {
		t.Fatalf("changed settings admitted the old evaluation: %v", err)
	}
	if !c.State().Decision.At.IsZero() {
		t.Fatal("invalidated evaluation published a decision")
	}
}

func TestSchedulerSnapshotPublicationNeverMovesBackwards(t *testing.T) {
	c := &reservationState{}
	latest := serving.SchedulerSnapshot{At: time.Now()}
	c.publishSnapshotLocked(&latest)
	old := serving.SchedulerSnapshot{At: latest.At.Add(-time.Second)}
	c.publishSnapshotLocked(&old)
	if !c.state.Snapshot.At.Equal(latest.At) {
		t.Fatal("snapshot publication moved time backwards")
	}
}

func TestSchedulerEvaluationAllowsPrepareToRouteProgress(t *testing.T) {
	c, prepared := tickReadySubsetController(t, nil)
	c.config.Settings.Controls.Routing = true
	c.runtime.EnableSchedulerRouting()
	snapshot, err := c.capture(context.Background(), time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	uid := snapshot.Workloads[0].UID
	destination := snapshot.Instances[1]
	destination.WorkloadUID = uid
	prepare := serving.Action{ID: "preparing-during-evaluation", Kind: servingstate.OperationPrepare,
		Node: destination.Node, Instance: destination.ID, Status: servingstate.NativeOperationRunning}
	route := serving.Action{ID: "route-after-prepare", Kind: servingstate.OperationRoute, DependsOn: []string{prepare.ID}}
	target := serving.SchedulerCandidate{ID: "progress-during-evaluation", Nodes: snapshot.Nodes,
		Instances: []serving.SchedulerInstance{destination}, Routing: map[string]map[string]int32{uid: {destination.ID: 1000}},
		Actions: []serving.Action{prepare, route}}
	c.state.Snapshot = snapshot
	installTestTransition(c, &target)
	c.runs[transitionRunKind+"/"+target.ID].admittedControls = c.config.Settings.Controls
	run := &actionRun{action: prepare, owner: target.ID, started: time.Now(),
		result: &servingstate.OperationResult{Operation: servingstate.Operation{ID: prepare.ID, Kind: prepare.Kind,
			Prepare: &servingstate.PrepareSpec{Instance: destination}}, Status: servingstate.NativeOperationRunning}}
	c.runs[prepare.ID] = run
	done, resume := holdSchedulerEvaluation(t, c)
	prepared.Store(true)
	receipt := *run.result
	receipt.Status, receipt.EndedAt = servingstate.NativeOperationComplete, time.Now()
	c.completeRun(run, &receipt, true, nil, c.ctx)
	continued := make(chan error, 1)
	go func() { continued <- c.Continue(context.Background(), time.Now()) }()
	select {
	case err := <-continued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		resume()
		<-done
		<-continued
		t.Fatal("prepare to route waited for evaluation")
	}
	c.wg.Wait()
	c.mu.Lock()
	routed := c.runs[route.ID]
	if routed == nil || routed.action.Status != servingstate.NativeOperationComplete {
		c.mu.Unlock()
		t.Fatalf("approved prepare did not progress to acknowledged route: %+v", routed)
	}
	at := c.state.Snapshot.At
	fence := c.routeWriters[uid][destination.ID]
	c.mu.Unlock()
	resume()
	if err := <-done; err != nil {
		t.Fatalf("progress during evaluation discarded it: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Snapshot.At.Before(at) || !c.routeWriters[uid][destination.ID].Equal(fence) || fence.IsZero() {
		t.Fatal("evaluation published a snapshot older than progress or erased its writer fence")
	}
}

// approvalOnLatestCapture is a controller whose published capture is newer than
// an evaluation input: it returns that input (snapshot) and a route target on
// the latest capture, after an unrelated owner completed and progress published.
func approvalOnLatestCapture(t *testing.T) (*SchedulerController, context.Context, serving.SchedulerSnapshot, serving.SchedulerCandidate, serving.SchedulerInstance) {
	t.Helper()
	c, _ := tickReadySubsetController(t, nil)
	c.config.Settings.AutonomousWorkloads = nil
	c.config.Settings.Controls.Routing = true
	c.runtime.EnableSchedulerRouting()
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	snapshot, err := c.captureReconciled(parent, time.Now(), c.config, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	uid := snapshot.Workloads[0].UID
	ready := snapshot.Instances[0]
	ready.WorkloadUID = uid
	seed := serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{ready},
		Routing: map[string]map[string]int32{uid: {ready.ID: 1000}}}
	if err := c.runtime.ApplySchedulerWorkloadsTarget(seed, []string{uid}); err != nil {
		t.Fatal(err)
	}
	slow := serving.NodeReference{Name: slowNodeName, UID: slowNodeName}
	c.mu.Lock()
	c.runs["unrelated"] = &actionRun{owner: "unrelated", reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{slow}},
		action: serving.Action{ID: "unrelated", Kind: measurementLeaseKind}, result: &servingstate.OperationResult{}}
	c.mu.Unlock()
	snapshot, err = c.captureReconciled(parent, time.Now(), c.config, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	target := serving.SchedulerCandidate{ID: "latest-capture-target", Nodes: snapshot.Nodes,
		Instances: slices.Clone(snapshot.Instances), Actions: []serving.Action{{ID: "latest-capture-route", Kind: servingstate.OperationRoute}},
		Routing: map[string]map[string]int32{uid: {snapshot.Instances[0].ID: 1000, snapshot.Instances[1].ID: 0}}}
	// During evaluation the unrelated owner completes and progress publishes.
	c.mu.Lock()
	c.releaseOwnerLocked("unrelated")
	c.mu.Unlock()
	if err := c.Continue(parent, time.Now()); err != nil {
		t.Fatal(err)
	}
	return c, parent, snapshot, target, ready
}

// A decision is evaluated once, on its own capture. While it was evaluated an
// unrelated owner completed and progress published. On the other Node an
// executor moved from its Pod to a Ready container: a target that only routes,
// or acts only on Node a, still launches. An executor on a Node the target acts
// on that became Ready since evaluation discards it.
func TestSchedulerApprovalChecksOnlyTheNodesTheTargetActsOn(t *testing.T) {
	podStage := func(i *serving.SchedulerInstance) { i.ID, i.Ready = i.PodUID, false }
	notReady := func(i *serving.SchedulerInstance) { i.Ready = false }
	for _, tc := range []struct {
		name      string
		actsOnA   bool
		instance  int // the instance the evaluated capture saw before it progressed
		evaluated func(*serving.SchedulerInstance)
		want      string // the discard reason; empty launches
	}{
		{"route only, other Node progressed", false, 1, podStage, ""},
		{"acts on Node a, other Node progressed", true, 1, podStage, ""},
		{"acts on Node a, its executor became Ready", true, 0, notReady, "planned Node changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, parent, snapshot, target, ready := approvalOnLatestCapture(t)
			if tc.actsOnA {
				target.Actions[0].Node = ready.Node
			}
			snapshot.Instances = slices.Clone(snapshot.Instances)
			routes := target.Routing[ready.WorkloadUID]
			weight := routes[target.Instances[tc.instance].ID]
			delete(routes, target.Instances[tc.instance].ID)
			tc.evaluated(&snapshot.Instances[tc.instance])
			tc.evaluated(&target.Instances[tc.instance])
			routes[target.Instances[tc.instance].ID] = weight
			event := &observation.SchedulerControlObservation{}
			c.tickMu.Lock()
			_, err := c.approveEvaluation(parent, time.Now(), c.config, snapshot.At, snapshot,
				serving.SchedulerDecision{At: snapshot.At, Target: &target}, event, func(string, *time.Duration) {})
			c.tickMu.Unlock()
			c.wg.Wait()
			c.mu.Lock()
			defer c.mu.Unlock()
			route := c.runs["latest-capture-route"]
			if tc.want == "" && (err != nil || len(event.DiscardedActions) != 0 || route == nil || route.action.Status != servingstate.NativeOperationComplete) {
				t.Fatalf("progress on a Node the target does not act on discarded it: %v %+v", err, route)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || route != nil) {
				t.Fatalf("a target launched although a Node it acts on changed: %v", err)
			}
		})
	}
}

// A decision that never launches keeps the target it chose in the record, at
// every one of the four places approval discards it.
func TestEveryDiscardRecordsTheChosenActions(t *testing.T) {
	for _, exit := range []struct {
		name, want string
		arrange    func(c *SchedulerController, snapshot *serving.SchedulerSnapshot, target *serving.SchedulerCandidate, config *SchedulerControllerConfig, ready serving.SchedulerInstance) (during func())
	}{
		{"planned Node changed", "planned Node changed", func(_ *SchedulerController, snapshot *serving.SchedulerSnapshot, target *serving.SchedulerCandidate, _ *SchedulerControllerConfig, ready serving.SchedulerInstance) func() {
			target.Actions[0].Node = ready.Node
			snapshot.Nodes = slices.Clone(snapshot.Nodes)
			for k := range snapshot.Nodes {
				if snapshot.Nodes[k].Node == ready.Node {
					snapshot.Nodes[k].PowerMode += "-changed"
				}
			}
			return nil
		}},
		{"configuration changed", "configuration changed", func(_ *SchedulerController, _ *serving.SchedulerSnapshot, _ *serving.SchedulerCandidate, config *SchedulerControllerConfig, _ serving.SchedulerInstance) func() {
			config.Settings.MaxReplicas++
			return nil
		}},
		{"live reservations changed", "live reservations changed", func(c *SchedulerController, _ *serving.SchedulerSnapshot, _ *serving.SchedulerCandidate, _ *SchedulerControllerConfig, ready serving.SchedulerInstance) func() {
			return func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				c.runs["taker"] = &actionRun{owner: "taker", reserved: true, scope: resourceScope{Nodes: []serving.NodeReference{ready.Node}},
					action: serving.Action{ID: "taker", Kind: measurementLeaseKind}, result: &servingstate.OperationResult{}}
			}
		}},
		{"persist decision", "persist decision before physical action", func(c *SchedulerController, _ *serving.SchedulerSnapshot, _ *serving.SchedulerCandidate, _ *SchedulerControllerConfig, _ serving.SchedulerInstance) func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.config.OnDecision = func(serving.SchedulerDecision, serving.SchedulerSnapshot) error { return errors.New("disk full") }
			return nil
		}},
	} {
		t.Run(exit.name, func(t *testing.T) {
			c, parent, snapshot, target, ready := approvalOnLatestCapture(t)
			config := c.config
			during := exit.arrange(c, &snapshot, &target, &config, ready)
			event := &observation.SchedulerControlObservation{}
			captured := false
			c.tickMu.Lock()
			_, err := c.approveEvaluation(parent, time.Now(), config, snapshot.At, snapshot,
				serving.SchedulerDecision{At: snapshot.At, Target: &target}, event, func(step string, _ *time.Duration) {
					// Between the fresh capture and the ledger read.
					captured = captured || step == "capture"
					if step == "publish" && captured && during != nil {
						during()
					}
				})
			c.tickMu.Unlock()
			c.wg.Wait()
			if err == nil || !strings.Contains(err.Error(), exit.want) {
				t.Fatalf("the exit %q was not taken: %v", exit.want, err)
			}
			if got := event.DiscardedActions; len(got) != 1 || got[0].ID != target.Actions[0].ID {
				t.Fatalf("the discarded decision's record lost the chosen actions: %+v", got)
			}
		})
	}
}
