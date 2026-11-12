package kube

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Block the real evaluator at its context boundary, without a controller hook.
type heldEvaluationContext struct {
	context.Context
	entered, release chan struct{}
	once             sync.Once
	function         string
}

func (c *heldEvaluationContext) Err() error {
	pc, _, _, _ := runtime.Caller(1)
	function := c.function
	if function == "" {
		function = ".evaluateScheduler"
	}
	if fn := runtime.FuncForPC(pc); fn != nil && strings.HasSuffix(fn.Name(), function) {
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

func TestSchedulerEvaluationDoesNotBlockContinuationOrOverwriteSnapshot(t *testing.T) {
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
	// A writer observed only after capture must survive stale publication.
	c.mu.Lock()
	c.routeWriters = map[string]servingstate.RouteWriters{"new-workload": {"new-instance": before.At}}
	c.mu.Unlock()
	resume()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "evaluation invalidated") {
		t.Fatalf("stale evaluation was not rejected: %v", err)
	}
	after := c.State().Snapshot
	if !after.At.Equal(before.At) || after.At.Before(before.At) {
		t.Fatalf("stale evaluation replaced progress snapshot: %s -> %s", before.At, after.At)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.routeWriters["new-workload"]["new-instance"].Equal(before.At) {
		t.Fatal("stale evaluation deleted the route writer fence")
	}
}

func TestSchedulerEvaluationRechecksApprovalInputs(t *testing.T) {
	for _, change := range []string{"settings", "reservation", "physical"} {
		t.Run(change, func(t *testing.T) {
			c := schedulerEmptyPoolController(t)
			if change == "physical" {
				// A live owner requires physical approval even in a Keep round.
				c.runs["measurement-owner"] = &actionRun{action: serving.Action{ID: "measurement-owner", Kind: measurementLeaseKind}}
			}
			done, resume := holdSchedulerEvaluation(t, c)
			switch change {
			case "settings":
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
			case "reservation":
				c.mu.Lock()
				c.runs["new-owner"] = &actionRun{owner: "new-owner", reserved: true,
					action: serving.Action{ID: "new-owner", Kind: measurementLeaseKind},
					scope:  resourceScope{Nodes: []serving.NodeReference{{Name: "node", UID: "node"}}}}
				c.mu.Unlock()
			case "physical":
				nodes, err := c.kube.CoreV1().Nodes().List(context.Background(), meta.ListOptions{})
				if err != nil {
					t.Fatal(err)
				}
				node := nodes.Items[0].DeepCopy()
				node.Status.Conditions[0].Status = core.ConditionFalse
				if _, err := c.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			resume()
			if err := <-done; err == nil || !strings.Contains(err.Error(), "evaluation invalidated") {
				t.Fatalf("changed %s admitted the old evaluation: %v", change, err)
			}
			if !c.State().Decision.At.IsZero() {
				t.Fatal("invalidated evaluation published a decision")
			}
		})
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
	if err := <-done; err == nil {
		t.Fatal("evaluation captured before prepare completion was approved")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Snapshot.At.Equal(at) || !c.routeWriters[uid][destination.ID].Equal(fence) || fence.IsZero() {
		t.Fatal("stale evaluation replaced progress or erased its writer fence")
	}
}

func TestSchedulerTargetAdmissionDoesNotBlockProgress(t *testing.T) {
	c := schedulerEmptyPoolController(t)
	snapshot, err := c.capture(context.Background(), time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	c.state.Snapshot = snapshot
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx := &heldEvaluationContext{Context: parent, entered: make(chan struct{}), release: make(chan struct{}),
		function: ".SchedulerTargetAdmission"}
	var release sync.Once
	resume := func() { release.Do(func() { close(ctx.release) }) }
	defer resume()
	done := make(chan error, 1)
	go func() {
		c.tickMu.Lock()
		target := serving.SchedulerCandidate{Keep: true, Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: snapshot.Routing}
		_, _, err := c.recheckEvaluationTarget(ctx, c.config, snapshot, target, &observation.SchedulerControlObservation{}, func(string, *time.Duration) {})
		c.tickMu.Unlock()
		done <- err
	}()
	select {
	case <-ctx.entered:
	case <-parent.Done():
		t.Fatal(parent.Err())
	}
	continued := make(chan error, 1)
	go func() { continued <- c.Continue(parent, time.Now()) }()
	select {
	case err := <-continued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		resume()
		<-done
		<-continued
		t.Fatal("Continue waited for frozen-target admission replay")
	}
	at := c.State().Snapshot.At
	resume()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "evaluation invalidated") {
		t.Fatalf("target admission overwrote concurrent progress: %v", err)
	}
	if !c.State().Snapshot.At.Equal(at) {
		t.Fatal("target admission replaced the progress snapshot")
	}
}

func TestSchedulerApprovalUsesAppliedOccupancyCells(t *testing.T) {
	for _, change := range []string{"provenance-refresh", "unselected-cell-change", "source-change", "applied-value-change", "configuration-provenance-refresh", "configuration-applied-value-change"} {
		t.Run(change, func(t *testing.T) {
			c := schedulerCaptureClockController(t, nil)
			snapshot, err := c.capture(context.Background(), time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			instance := snapshot.Instances[0]
			condition := instance.Profile.Condition
			at := time.Now()
			initial := []serving.DispatchOccupancy{{
				Node: instance.Node, PowerMode: condition.PowerMode, Model: condition.LogicalModel,
				ProfileID: instance.Profile.ID, ExecutionPath: condition.Artifact.ExecutionPath, Batch: instance.MaxBatch,
				RuntimeImage: condition.RuntimeImage,
				PerRequest:   time.Millisecond, Source: "original-record",
				Provenance: &serving.DispatchOccupancyProvenance{Source: "measured", UpdatedAt: at, Pairs: 10, Value: int64(time.Millisecond)},
			}, {
				Node: serving.NodeReference{Name: "unused", UID: "unused"}, PerRequest: time.Second,
			}}
			var occupancy atomic.Value
			occupancy.Store(initial)
			c.config.DispatchOccupancySnapshot = func() []serving.DispatchOccupancy {
				return occupancy.Load().([]serving.DispatchOccupancy)
			}
			configurationChange := strings.HasPrefix(change, "configuration-")
			if configurationChange {
				c.config.DispatchOccupancySnapshot = nil
				c.config.DispatchOccupancy = initial
			}
			c.runs["measurement-owner"] = &actionRun{action: serving.Action{ID: "measurement-owner", Kind: measurementLeaseKind}}
			done, resume := holdSchedulerEvaluation(t, c)
			updated := append([]serving.DispatchOccupancy(nil), initial...)
			provenance := *initial[0].Provenance
			updated[0].Provenance = &provenance
			switch strings.TrimPrefix(change, "configuration-") {
			case "provenance-refresh":
				provenance.UpdatedAt = at.Add(time.Second)
				provenance.Pairs++
				provenance.Window = &serving.OccupancyWindow{}
				updated[0].Source = "refreshed-record"
			case "unselected-cell-change":
				updated[1].PerRequest++
			case "source-change":
				provenance.Source = "dom"
			case "applied-value-change":
				updated[0].PerRequest++
				provenance.Value++
			}
			if configurationChange {
				config := c.config
				config.DispatchOccupancy = updated
				if err := c.SetConfiguration(config); err != nil {
					t.Fatal(err)
				}
			} else {
				occupancy.Store(updated)
			}
			resume()
			err = <-done
			if strings.HasSuffix(change, "applied-value-change") {
				if err == nil || !strings.Contains(err.Error(), "evaluation invalidated") {
					t.Fatalf("changed applied occupancy was approved: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unchanged applied occupancy was discarded after %s: %v", change, err)
			}
		})
	}
}
