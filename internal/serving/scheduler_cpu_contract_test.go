package serving

import (
	"slices"
	"testing"
	"time"
)

func measuredWideAffinityFixture() (SchedulerSettings, SchedulerSnapshot) {
	settings, snapshot := schedulerFixture()
	settings.Controls.Enabled = false
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
	*instance = schedulerMeasuredInstance(*instance, snapshot.Workloads[0], 0)
	snapshot.Arrivals = []SchedulerArrival{{ID: "measured-arrival", At: snapshot.At.Add(-settings.ObservationWindow), Definition: snapshot.Workloads[0], Compute: true, Accepted: true}}
	return settings, snapshot
}

func TestSchedulerMeasuredQuotaAndThreadsNeedNotFillAffinity(t *testing.T) {
	settings, snapshot := measuredWideAffinityFixture()
	decision := schedulerEvaluateForTest(t, settings, snapshot)
	if decision.Keep.ExpectedJoules == nil || len(decision.Keep.Reasons) != 0 {
		t.Fatalf("admitted measured allocation did not reach replay: %+v", decision.Keep)
	}
	estimate := schedulerRequireEstimate(t, decision.Keep, snapshot.Workloads[0].UID)
	if estimate.Samples != settings.Replays || estimate.ExpectedNanos == nil || *estimate.ExpectedNanos < float64(time.Millisecond) {
		t.Fatalf("measured service did not replay: %+v", estimate)
	}
	allocation := decision.Keep.Instances[0].Allocation
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
			decision := schedulerEvaluateForTest(t, settings, snapshot)
			schedulerRequireReason(t, decision.Keep.Reasons, tc.reason)
		})
	}
}

func TestSchedulerMeasuredCPUQuantitiesRemainDistinctInLogicalKey(t *testing.T) {
	_, snapshot := measuredWideAffinityFixture()
	candidate := SchedulerCandidate{Nodes: snapshot.Nodes, Instances: snapshot.Instances}
	before, err := SchedulerLogicalConfigKey(snapshot, candidate)
	if err != nil {
		t.Fatal(err)
	}
	for _, quantity := range []string{"request", "affinity", "threads", "profile"} {
		t.Run(quantity, func(t *testing.T) {
			_, changed := measuredWideAffinityFixture()
			instance := &changed.Instances[0]
			switch quantity {
			case "request":
				instance.Allocation.CPUCount = 1
			case "affinity":
				instance.Allocation.CPUs = []int{0, 1}
			case "threads":
				instance.Allocation.Threads = 1
			case "profile":
				instance.Profile.ID = "different-measured-quota"
			}
			after, err := SchedulerLogicalConfigKey(changed, SchedulerCandidate{Nodes: changed.Nodes, Instances: changed.Instances})
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatalf("logical key merged distinct measured %s", quantity)
			}
		})
	}
}
