package kube

import (
	"context"
	"testing"
	"time"

	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestCommonReservationConflictAndReleaseAC38(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &SchedulerController{ctx: ctx, runs: map[string]*actionRun{}}
	n1 := serving.NodeReference{Name: "one", UID: "one-uid"}
	n2 := serving.NodeReference{Name: "two", UID: "two-uid"}
	release := make(chan struct{})
	done, _ := c.startNodeRun(Operation{ID: "route", Kind: operationRoute, Node: n1, WorkloadUID: "w1"}, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error { <-release; return nil })
	for _, operation := range []Operation{
		{ID: "power", Kind: operationPower, Node: n1},
		{ID: "reset", Kind: operationReset, WorkloadUIDs: []string{"w1"}},
		{ID: "occupancy", Kind: occupancyKind, Node: n2, WorkloadUID: "w1"},
	} {
		ended, result := c.startNodeRun(operation, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error { t.Error("conflicting operation executed"); return nil })
		<-ended
		if result.Status != nativeOperationFailed {
			t.Fatalf("conflict admitted: %+v", result)
		}
	}
	independent, _ := c.startNodeRun(Operation{ID: "independent", Kind: operationPrepare, Node: n2, WorkloadUID: "w2"}, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error { return nil })
	<-independent
	close(release)
	<-done
	after, result := c.startNodeRun(Operation{ID: "after", Kind: operationPower, Node: n1}, SchedulerControllerConfig{}, func(context.Context, *OperationResult) error { return nil })
	<-after
	if result.Status != nativeOperationComplete {
		t.Fatalf("returned reservation remained held: %+v", result)
	}
}

func TestTerminatedRunRetainsReservationUntilPhysicalReturnAC38(t *testing.T) {
	for _, termination := range []string{"timeout", "cancel"} {
		t.Run(termination, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &SchedulerController{ctx: ctx, runs: map[string]*actionRun{}}
			node := serving.NodeReference{Name: "one", UID: "one-uid"}
			done, result := c.startNodeRun(Operation{ID: "remote", Kind: operationPrepare, Node: node, PodUID: "pod"}, SchedulerControllerConfig{}, func(ctx context.Context, _ *OperationResult) error {
				if termination == "timeout" {
					return context.DeadlineExceeded
				}
				<-ctx.Done()
				return ctx.Err()
			})
			if termination == "cancel" {
				c.mu.Lock()
				c.runs["remote"].cancel()
				c.mu.Unlock()
			}
			<-done
			if result.Status != nativeOperationFailed {
				t.Fatalf("execution did not terminate: %+v", result)
			}
			next := Operation{ID: "next", Kind: operationPower, Node: node}
			assertHeld := func() {
				t.Helper()
				if err := c.authorize(next); err == nil {
					t.Fatal("unconfirmed remote effect released reservation")
				}
			}
			assertHeld()
			observed := result.EndedAt.Add(time.Second)
			settled := core.Node{ObjectMeta: meta.ObjectMeta{Name: node.Name, UID: "one-uid"}}
			pod := core.Pod{ObjectMeta: meta.ObjectMeta{UID: "pod"}, Spec: core.PodSpec{NodeName: node.Name}}
			c.releaseEmptyScopes([]core.Node{settled}, []core.Pod{pod}, observed)
			assertHeld()
			pending := settled.DeepCopy()
			pending.Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
			c.releaseEmptyScopes([]core.Node{*pending}, nil, observed)
			assertHeld()
			// A fresh observation after reset/cleanup proves both Pod absence
			// and settled power; execution termination alone never does.
			c.releaseEmptyScopes([]core.Node{settled}, nil, observed)
			if err := c.authorize(next); err != nil {
				t.Fatalf("physical-return proof did not release reservation: %v", err)
			}
			c.wg.Wait()
		})
	}
}
