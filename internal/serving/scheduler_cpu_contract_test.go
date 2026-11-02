package serving

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestSchedulerPowerStateConstructorsNeedNoCPUInventory(t *testing.T) {
	node := SchedulerNode{Node: NodeReference{Name: "node", UID: "node"}, PowerMode: "two"}
	action := Action{Kind: schedulerActionPower, Node: node.Node, To: "four", Fixed: true, Status: schedulerActionPlanned}
	common := schedulerCommonNodes(SchedulerSnapshot{Nodes: []SchedulerNode{node}, Actions: []Action{action}})
	replay := schedulerReplayState{nodes: []SchedulerNode{node}, actions: []*schedulerReplayAction{{action: action, started: true}}}
	if !replay.completeActions() || common[0].PowerMode != action.To || replay.nodes[0].PowerMode != action.To || node.PowerMode != "two" {
		t.Fatalf("CPU inventory blocked a power projection or mutated source: %+v %+v", common, replay)
	}
}

func TestSchedulerSharedCPUObservationsAreNotPlacementAuthority(t *testing.T) {
	for _, threads := range []int{0, 2, 8} {
		settings, snapshot := schedulerFixture()
		settings.AutonomousWorkloads = nil
		metadata := *snapshot.Instances[0].Profile.RuntimeMetadata
		metadata.IntraOpThreads, metadata.InterOpThreads = threads, 0
		metadata.CPUAffinity, metadata.CgroupCPUMax = []int{7}, "max 100000"
		snapshot.Instances[0].Profile.RuntimeMetadata = &metadata
		snapshot.Arrivals = []SchedulerArrival{{ID: "arrival", At: snapshot.At.Add(-settings.ObservationWindow), Definition: snapshot.Workloads[0], Compute: true, Accepted: true}}
		keep := schedulerKeepForTest(t, settings, snapshot)
		if keep.ExpectedJoules == nil || len(keep.Reasons) != 0 {
			t.Fatalf("shared runtime observation blocked replay: %+v", keep)
		}
		estimate := schedulerRequireEstimate(t, keep, snapshot.Workloads[0].UID)
		if estimate.Samples != 1 || estimate.ExpectedNanos == nil || *estimate.ExpectedNanos < float64(time.Millisecond) {
			t.Fatalf("measured service missing: %+v", estimate)
		}
	}
}

func TestSchedulerSharedCPURetainsResourceGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SchedulerSnapshot)
		reason string
	}{
		{"memory", func(s *SchedulerSnapshot) { s.Nodes[0].MemoryBytes = 999 }, "MemoryBudgetInfeasible"},
		{"measured memory", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.MemoryBytes = 1 }, "MeasuredMemoryInfeasible"},
		{"GPU inventory", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.GPUUUID = "missing" }, "ExclusiveGPUInfeasible"},
		{"GPU duplicate", func(s *SchedulerSnapshot) {
			s.Nodes[0].GPUUUIDs = []string{"gpu"}
			s.Nodes[0].Reserved.GPUUUID = "gpu"
			s.Instances[0].Allocation.GPUUUID = "gpu"
		}, "ExclusiveGPUInfeasible"},
		{"not ready", func(s *SchedulerSnapshot) { s.Nodes[0].Ready = false }, "NodeUnavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			tc.change(&snapshot)
			decision, err := EvaluateScheduler(context.Background(), settings, snapshot, nil)
			if err != nil {
				t.Fatal(err)
			}
			schedulerRequireReason(t, decision.Keep.Reasons, tc.reason)
			if decision.Status != schedulerStatusPending || decision.Target != nil {
				t.Fatalf("unsafe budget executable: %+v", decision)
			}
		})
	}
	settings, snapshot := schedulerFixture()
	snapshot.Nodes[0].PowerMode = ""
	snapshot.Nodes[0].Reasons = []string{NodeInstanceUnavailable}
	snapshot.Instances[0].Ready = false
	if reasons := schedulerValidateResources(settings, snapshot.Nodes, snapshot.Instances); len(reasons) != 0 {
		t.Fatal(reasons)
	}
	snapshot.Nodes[0].Reserved.MemoryBytes = snapshot.Nodes[0].MemoryBytes
	if reasons := schedulerValidateResources(settings, snapshot.Nodes, snapshot.Instances); !slices.Contains(reasons, "MemoryBudgetInfeasible:"+snapshot.Nodes[0].Node.UID) {
		t.Fatal("retained occupancy lost memory safety", reasons)
	}
}
