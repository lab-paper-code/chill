package kube

import (
	"context"
	"testing"

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
