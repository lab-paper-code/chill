package serving

import (
	"context"
	"slices"
	"testing"
	"time"
)

const schedulerUnknownModeForTest = "unknown"

func TestSchedulerNodeAtModeProjectsPowerAndCPUsTogether(t *testing.T) {
	node := SchedulerNode{
		PowerMode: "two", CPUs: []int{0, 1},
		ModeCPUs: map[string][]int{"two": {0, 1}, "four": {0, 1, 2, 3}, "six": {0, 1, 2, 3, 4, 5}},
		Reserved: SchedulerAllocation{CPUs: []int{0, 1}},
	}
	for _, mode := range []string{"two", "four", "six", schedulerUnknownModeForTest} {
		got, known := node.AtMode(mode)
		want := node.CPUs
		if mode != "two" && mode != schedulerUnknownModeForTest {
			want = node.ModeCPUs[mode]
		}
		if known != (mode != schedulerUnknownModeForTest) || !slices.Equal(got.CPUs, want) ||
			known && got.PowerMode != mode || !known && got.PowerMode != node.PowerMode ||
			!slices.Equal(got.Reserved.CPUs, node.Reserved.CPUs) {
			t.Fatalf("mode and CPU inventory must be one projection: mode=%s known=%t node=%+v", mode, known, got)
		}
		if node.PowerMode != "two" || !slices.Equal(node.CPUs, []int{0, 1}) {
			t.Fatalf("projection changed the source inventory: %+v", node)
		}
	}
	projected, _ := node.AtMode("four")
	back, known := projected.AtMode("two")
	if !known || back.PowerMode != node.PowerMode || !slices.Equal(back.CPUs, node.CPUs) {
		t.Fatalf("round trip lost observed inventory: %+v", back)
	}
	unconfirmed := SchedulerNode{PowerMode: "two", CPUs: node.CPUs}
	if _, known := unconfirmed.AtMode("two"); known {
		t.Fatal("a mode label without normalized inventory must not certify its CPUs")
	}
}

func TestSchedulerPowerStateConstructorsProjectInventory(t *testing.T) {
	node := SchedulerNode{
		Node: NodeReference{Name: "node", UID: "node"}, PowerMode: "two", CPUs: []int{0, 1},
		ModeCPUs: map[string][]int{"four": {0, 1, 2, 3}},
	}
	for _, mode := range []string{"four", schedulerUnknownModeForTest} {
		action := Action{Kind: schedulerActionPower, Node: node.Node, To: mode, Fixed: true, Status: schedulerActionPlanned}
		common := schedulerCommonNodes(SchedulerSnapshot{Nodes: []SchedulerNode{node}, Actions: []Action{action}})
		replay := schedulerReplayState{
			nodes:   []SchedulerNode{node},
			actions: []*schedulerReplayAction{{action: action, started: true}},
		}
		completed := replay.completeActions()
		if completed != (mode != schedulerUnknownModeForTest) || mode == schedulerUnknownModeForTest && (replay.actions[0].done || len(replay.out.reasons) == 0 ||
			!slices.Contains(common[0].Reasons, NodePlacementInventoryMissing)) {
			t.Fatalf("unknown power inventory was completed or lost its reason: %+v", replay)
		}
		wantMode, wantCPUs := mode, node.ModeCPUs[mode]
		if mode == schedulerUnknownModeForTest {
			wantMode, wantCPUs = node.PowerMode, node.CPUs
		}
		for _, got := range []SchedulerNode{common[0], replay.nodes[0]} {
			if got.PowerMode != wantMode || !slices.Equal(got.CPUs, wantCPUs) {
				t.Fatalf("constructor split mode from inventory: target=%s node=%+v", mode, got)
			}
		}
	}
}

func TestSchedulerRetainedOccupancyUsesObservedBudgetWithoutTargetMode(t *testing.T) {
	settings, snapshot := schedulerFixture()
	node := &snapshot.Nodes[0]
	node.PowerMode, node.ModeCPUs = "", nil
	node.Reasons = []string{NodeInstanceUnavailable}
	snapshot.Instances[0].Ready = false
	if reasons := schedulerValidateResources(settings, snapshot.Nodes, snapshot.Instances); len(reasons) != 0 {
		t.Fatalf("retained physical occupancy invented a target projection: %v", reasons)
	}
	node.Reserved.CPUs = snapshot.Instances[0].Allocation.CPUs
	reasons := schedulerValidateResources(settings, snapshot.Nodes, snapshot.Instances)
	schedulerRequireReason(t, reasons, "ExclusiveCPUInfeasible")
}

func measuredWideAffinityFixture() (SchedulerSettings, SchedulerSnapshot) {
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	snapshot.Options = nil
	instance := &snapshot.Instances[0]
	profile := *instance.Profile
	metadata := *profile.RuntimeMetadata
	profile.Condition.CPU.Request, profile.Condition.CPU.Limit = "1500m", "2"
	profile.Condition.CPU.ORTIntraOpThreads = 2
	profile.Condition.CPU.AffinityCPUs = []int{0, 1, 2, 3}
	metadata.CPUAffinity, metadata.IntraOpThreads = slices.Clone(profile.Condition.CPU.AffinityCPUs), 2
	metadata.CgroupCPUMax = "200000 100000"
	profile.RuntimeMetadata = &metadata
	instance.Profile = &profile
	instance.Allocation.CPUs, instance.Allocation.CPUCount, instance.Allocation.Threads = slices.Clone(metadata.CPUAffinity), 2, 2
	snapshot.Arrivals = []SchedulerArrival{{ID: "measured-arrival", At: snapshot.At.Add(-settings.ObservationWindow), Definition: snapshot.Workloads[0], Compute: true, Accepted: true}}
	return settings, snapshot
}

func TestSchedulerMeasuredQuotaAndThreadsNeedNotFillAffinity(t *testing.T) {
	settings, snapshot := measuredWideAffinityFixture()
	keep := schedulerKeepForTest(t, settings, snapshot)
	if keep.ExpectedJoules == nil || len(keep.Reasons) != 0 {
		t.Fatalf("admitted measured allocation did not reach replay: %+v", keep)
	}
	estimate := schedulerRequireEstimate(t, keep, snapshot.Workloads[0].UID)
	if estimate.Samples != 1 || estimate.ExpectedNanos == nil || *estimate.ExpectedNanos < float64(time.Millisecond) {
		t.Fatalf("measured service did not replay: %+v", estimate)
	}
	allocation := keep.Instances[0].Allocation
	if allocation.CPUCount != 2 || allocation.Threads != 2 || !slices.Equal(allocation.CPUs, []int{0, 1, 2, 3}) {
		t.Fatalf("replay rewrote the measured CPU contract: %+v", allocation)
	}
}

func TestSchedulerMeasuredWideAffinityRetainsResourceGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SchedulerSnapshot)
		reason string
	}{
		{"zero threads", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.Threads = 0 }, "CPUThreadAllocationMismatch"},
		{"too many threads", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.Threads = 5 }, "CPUThreadAllocationMismatch"},
		{"unmeasured threads", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.Threads = 3 }, "CPUThreadAllocationMismatch"},
		{"unmeasured mask", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.CPUs = []int{0, 1} }, "MeasuredAffinityMismatch"},
		{"implicit measured mask", func(s *SchedulerSnapshot) {
			s.Instances[0].Profile.Condition.CPU.AffinityCPUs = nil
			s.Instances[0].Allocation.CPUs = []int{0, 1}
		}, "MeasuredAffinityMismatch"},
		{"duplicate mask", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.CPUs = []int{0, 1, 2, 2} }, "ExclusiveCPUInfeasible"},
		{"overlapping instance", func(s *SchedulerSnapshot) {
			other := s.Instances[0]
			other.ID = "overlapping"
			s.Instances = append(s.Instances, other)
		}, "ExclusiveCPUInfeasible"},
		{"reserved CPU", func(s *SchedulerSnapshot) { s.Nodes[0].Reserved.CPUs = []int{3} }, "ExclusiveCPUInfeasible"},
		{"unavailable Node retains global exclusive guard AC34", func(s *SchedulerSnapshot) {
			s.Nodes[0].Reasons = []string{NodeInstanceUnavailable}
			s.Nodes[0].Reserved.CPUs = []int{3}
		}, "ExclusiveCPUInfeasible"},
		{"request exceeds mask", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.CPUCount = 5 }, "UnknownExclusiveCPUAffinity"},
		{"request exceeds budget", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.CPUCount = 5 }, "CPUBudgetInfeasible"},
		{"reserved quota oversubscribed", func(s *SchedulerSnapshot) {
			s.Nodes[0].NonServing = []SchedulerAllocation{{CPUCount: 1, CPUReserved: true}}
		}, "CPUBudgetInfeasible"},
		{"memory oversubscribed", func(s *SchedulerSnapshot) { s.Nodes[0].MemoryBytes = 999 }, "MemoryBudgetInfeasible"},
		{"unknown GPU", func(s *SchedulerSnapshot) { s.Instances[0].Allocation.GPUUUID = "unavailable" }, "ExclusiveGPUInfeasible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, snapshot := measuredWideAffinityFixture()
			tc.change(&snapshot)
			decision, err := EvaluateScheduler(context.Background(), settings, snapshot, nil)
			if err != nil {
				t.Fatal(err)
			}
			schedulerRequireReason(t, decision.Keep.Reasons, tc.reason)
			if decision.Status != schedulerStatusPending || decision.Target != nil {
				t.Fatalf("invalid global resource budget became executable: %+v", decision)
			}
		})
	}
}
