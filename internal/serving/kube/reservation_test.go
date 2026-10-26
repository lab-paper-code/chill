package kube

import (
	"context"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
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
			c.releaseObservedScopes([]core.Node{settled}, []core.Pod{pod}, nil, observed)
			assertHeld()
			pending := settled.DeepCopy()
			pending.Labels = map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending}
			c.releaseObservedScopes([]core.Node{*pending}, nil, nil, observed)
			assertHeld()
			// A fresh observation after reset/cleanup proves both Pod absence
			// and settled power; execution termination alone never does.
			c.releaseObservedScopes([]core.Node{settled}, nil, nil, observed)
			if err := c.authorize(next); err != nil {
				t.Fatalf("physical-return proof did not release reservation: %v", err)
			}
			c.wg.Wait()
		})
	}
}

func TestFailedManualRoutingReleasesOnlyFreshKnownEffectsAC38(t *testing.T) {
	for _, evidence := range []string{"declaration", "previous", "mismatch", "stale", "unready", "native-unready", "restarted", "pod-unready", "unknown-pod"} {
		t.Run(evidence, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pod, node, profile, _ := observerSourceFixture()
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
			wanted := serving.SchedulerInstance{ID: id, Node: ref, Profile: &profile, WorkloadUID: "workload", MaxBatch: 2}
			target := serving.SchedulerCandidate{ID: "new-policy", Instances: []serving.SchedulerInstance{wanted}}
			previous := native.Metadata{ProfileID: profile.ID, DecisionID: "old-policy", Policy: native.Policy{ProfileID: profile.ID, DecisionID: "old-policy", MaxBatchSize: 1}}
			c := &SchedulerController{ctx: ctx, runs: map[string]*actionRun{}}
			c.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: id, PodUID: string(pod.UID), Node: ref, WorkloadUID: "workload"}}
			done, result := c.startNodeRun(Operation{ID: "manual-route:test", Kind: operationRoute, WorkloadUID: "workload", Target: &target}, SchedulerControllerConfig{}, func(_ context.Context, result *OperationResult) error {
				result.Before.Native = map[string]native.State{id: {Ready: true, Metadata: previous}}
				result.Status = nativeOperationPartial
				return context.DeadlineExceeded
			})
			<-done
			next := Operation{WorkloadUID: "workload"}
			if err := c.authorize(next); err == nil {
				t.Fatal("failed execution alone released reservation")
			}
			observed := result.EndedAt.Add(time.Second)
			state := serving.InstanceState{ID: id, PodUID: string(pod.UID), Node: ref, Ready: true, ObservedAt: observed, Native: &native.State{Ready: true, Metadata: native.Metadata{
				ProfileID: profile.ID, DecisionID: target.ID, Policy: native.Policy{ProfileID: profile.ID, DecisionID: target.ID, MaxBatchSize: wanted.MaxBatch},
			}}}
			switch evidence {
			case "previous":
				state.Native.Metadata = previous
			case "mismatch":
				state.Native.Metadata.Policy.MaxBatchSize++
			case "stale":
				state.ObservedAt = result.EndedAt
			case "unready":
				state.Ready = false
			case "native-unready":
				state.Native.Ready = false
			case "restarted":
				pod.Status.ContainerStatuses[0].ContainerID += "-new"
			case "pod-unready":
				pod.Status.ContainerStatuses[0].Ready = false
			case "unknown-pod":
				pod.UID = "manual-route-replacement"
				pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", UID: "workload"}}
			}
			c.releaseObservedScopes([]core.Node{*node}, []core.Pod{*pod}, []serving.InstanceState{state}, observed)
			released := c.authorize(next) == nil
			wantReleased := evidence == "declaration" || evidence == "previous"
			if released != wantReleased {
				t.Fatalf("evidence %s: released=%t, want %t", evidence, released, wantReleased)
			}
			c.wg.Wait()
		})
	}
}
