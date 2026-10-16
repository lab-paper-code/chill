package serving

import (
	"slices"
	"testing"
	"time"
)

// The observed CPU1 placements differed only in profile-derived memory, while
// the current physical runner already served two independently identified UIDs.
func placementOnlyFixture() (SchedulerSettings, SchedulerSnapshot) {
	settings, snapshot := schedulerFixture()
	settings.Controls = SchedulerControls{Enabled: true, Placement: true}
	settings.MaxCandidates = 128
	first := snapshot.Workloads[0]
	second := *first
	second.Name, second.UID = "second", "w2"
	snapshot.Workloads = append(snapshot.Workloads, &second)
	current := snapshot.Instances[0]
	current.Profile.Condition.Firstfloor.MaxBatchSize = 2
	current.Profile.Condition.PlannedBatchSizes = []int{1, 2}
	current.Profile.Batches = append(current.Profile.Batches, BatchProfile{Batch: 2, DurationNanos: []float64{float64(20 * time.Millisecond)}, MemoryBytes: 153178112})
	current.Profile.RuntimeMetadata.InitializedBatches = []int{1, 2}
	current.Profile.Condition.CPU.AffinityCPUs = []int{3}
	current.Profile.RuntimeMetadata.CPUAffinity = []int{3}
	current.Allocation.CPUs, current.Allocation.CPUCount = []int{3}, 1
	current.Allocation.MemoryBytes = 295698432
	current.MaxBatch, current.Weight = 2, 1
	current.WorkloadUIDs = []string{first.UID, second.UID}
	current = schedulerMeasuredInstance(current, first, 0)
	snapshot.Instances = []SchedulerInstance{current}
	snapshot.Routing = map[string]map[string]int32{first.UID: {current.ID: 1}, second.UID: {current.ID: 1}}
	snapshot.Nodes[0].MemoryBytes = 8 << 30
	snapshot.Nodes[0].Reserved.CPUs = []int{0, 1}
	destination := snapshot.Nodes[0]
	destination.Node = NodeReference{Name: "destination", UID: "n2"}
	snapshot.Nodes = append(snapshot.Nodes, destination)
	remote := current
	profile := *current.Profile
	profile.ID, profile.NodeName, profile.Condition.NodeUID = "destination-cpu1", destination.Node.Name, destination.Node.UID
	remote.ID, remote.Node, remote.Profile = "destination-slot", destination.Node, &profile
	remote.Allocation.MemoryBytes = 292552704
	remote = schedulerMeasuredInstance(remote, first, 0)
	snapshot.Options = nil
	for _, cpu1 := range []SchedulerInstance{current, remote} {
		snapshot.Options = append(snapshot.Options, SchedulerOption{Instance: cpu1, CompatibleUIDs: []string{first.UID, second.UID}})
		cpu2 := cpu1
		profile := *cpu1.Profile
		metadata := *profile.RuntimeMetadata
		profile.ID += "-cpu2"
		profile.Condition.CPU.AffinityCPUs = []int{2, 3}
		profile.Condition.CPU.ORTIntraOpThreads = 2
		profile.Condition.CPU.Request, profile.Condition.CPU.Limit = "2", "2"
		metadata.CPUAffinity, metadata.IntraOpThreads = []int{2, 3}, 2
		profile.RuntimeMetadata = &metadata
		cpu2.Profile = &profile
		cpu2.Allocation.CPUs, cpu2.Allocation.CPUCount, cpu2.Allocation.Threads = []int{2, 3}, 2, 2
		cpu2 = schedulerMeasuredInstance(cpu2, first, 0)
		snapshot.Options = append(snapshot.Options, SchedulerOption{Instance: cpu2, CompatibleUIDs: []string{first.UID, second.UID}})
	}
	return settings, snapshot
}

func TestPlacementOnlyMovesExistingSharedCPU1WithoutChangingOtherControls(t *testing.T) {
	settings, snapshot := placementOnlyFixture()
	candidates, _ := schedulerEnumerateForTest(t, settings, snapshot)
	found := false
	for _, candidate := range candidates {
		if len(candidate.Instances) != 1 {
			t.Fatalf("placement-only split the existing shared physical runner: %+v", candidate.Instances)
		}
		instance := candidate.Instances[0]
		if !slices.Equal(instance.WorkloadUIDs, snapshot.Instances[0].WorkloadUIDs) || len(instance.Allocation.CPUs) != 1 || instance.Allocation.CPUCount != 1 || instance.Allocation.Threads != 1 || instance.MaxBatch != 2 || instance.MaxWait != 0 {
			t.Fatalf("placement-only changed a disabled control: %+v", instance)
		}
		for _, workload := range snapshot.Workloads {
			if len(candidate.Routing[workload.UID]) != 1 || candidate.Routing[workload.UID][instance.ID] != 1 {
				t.Fatalf("placement-only changed shared routing weights: %v", candidate.Routing)
			}
		}
		if instance.Node != snapshot.Nodes[1].Node {
			continue
		}
		found = true
		if instance.ID == snapshot.Instances[0].ID || instance.Allocation.MemoryBytes != 292552704 {
			t.Fatalf("destination reused source physical identity or synthetic memory: %+v", instance)
		}
		if reasons := schedulerValidateResources(settings, candidate.Nodes, candidate.Instances); len(reasons) != 0 {
			t.Fatalf("measured destination failed unchanged resource validation: %v", reasons)
		}
		constrained := append([]SchedulerNode(nil), candidate.Nodes...)
		constrained[1].MemoryBytes = instance.Allocation.MemoryBytes - 1
		schedulerRequireReason(t, schedulerValidateResources(settings, constrained, candidate.Instances), "MemoryBudgetInfeasible")
		if !slices.ContainsFunc(candidate.Actions, func(action Action) bool { return action.Kind == schedulerActionPrepare && action.Node == instance.Node }) {
			t.Fatalf("destination is not an ordinary measured preparation action: %+v", candidate.Actions)
		}
	}
	if !found {
		t.Fatalf("placement-only omitted the measured CPU1 destination: %+v", candidates)
	}
}

func TestSharingOffPreservesCohortsWhileResourcesAndReplicasVary(t *testing.T) {
	for _, shape := range []string{"shared", "exclusive", "overlapping"} {
		settings, snapshot := placementOnlyFixture()
		settings.Controls.Resources, settings.Controls.Replicas = true, true
		if shape != "shared" {
			first := snapshot.Instances[0]
			second := snapshot.Options[2].Instance
			second.ID, second.WorkloadUIDs = "second-physical", []string{snapshot.Workloads[0].UID}
			if shape == "exclusive" {
				first.WorkloadUIDs = []string{snapshot.Workloads[0].UID}
				second.WorkloadUIDs = []string{snapshot.Workloads[1].UID}
				snapshot.Routing = map[string]map[string]int32{
					snapshot.Workloads[0].UID: {first.ID: 1},
					snapshot.Workloads[1].UID: {second.ID: 1},
				}
			} else {
				snapshot.Routing[snapshot.Workloads[0].UID][second.ID] = 1
			}
			snapshot.Instances = []SchedulerInstance{first, second}
		}
		candidates, _ := schedulerEnumerateForTest(t, settings, snapshot)
		if len(candidates) < 2 {
			t.Fatal("disabled sharing prevented independent resource/replica alternatives")
		}
		for _, candidate := range candidates {
			counts := map[string]int{}
			for _, instance := range candidate.Instances {
				if !slices.ContainsFunc(snapshot.Instances, func(original SchedulerInstance) bool {
					return slices.Equal(instance.WorkloadUIDs, original.WorkloadUIDs)
				}) {
					t.Fatalf("sharing OFF merged/split an existing cohort: %+v", instance.WorkloadUIDs)
				}
				for _, uid := range instance.WorkloadUIDs {
					counts[uid]++
				}
			}
			for _, count := range counts {
				if count > settings.MaxReplicas {
					t.Fatalf("grouped replica enumeration exceeded the per-UID bound: %v", counts)
				}
			}
		}
	}
}

func TestPlacementOnlyUsesDestinationPhysicalAffinityWithoutResizing(t *testing.T) {
	settings, snapshot := placementOnlyFixture()
	for index := range snapshot.Options {
		option := &snapshot.Options[index]
		if option.Instance.Allocation.CPUCount != 1 {
			continue
		}
		profile := *option.Instance.Profile
		metadata := *profile.RuntimeMetadata
		profile.ID += "-cpu-id-2"
		profile.Condition.CPU.AffinityCPUs, metadata.CPUAffinity = []int{2}, []int{2}
		profile.RuntimeMetadata = &metadata
		option.Instance.Profile, option.Instance.Allocation.CPUs = &profile, []int{2}
		option.Instance = schedulerMeasuredInstance(option.Instance, snapshot.Workloads[0], 0)
	}
	candidates, _ := schedulerEnumerateForTest(t, settings, snapshot)
	found := false
	for _, candidate := range candidates {
		instance := candidate.Instances[0]
		if candidate.Keep {
			continue
		}
		if instance.Node == snapshot.Nodes[0].Node {
			t.Fatal("Resources OFF changed physical affinity on the same Node")
		}
		if !slices.Equal(instance.Allocation.CPUs, []int{2}) || instance.Allocation.CPUCount != 1 || instance.Allocation.Threads != 1 {
			t.Fatalf("placement changed CPU quantity/threads instead of Node-local identity: %+v", instance.Allocation)
		}
		found = true
	}
	if !found {
		t.Fatal("source Node's physical CPU IDs incorrectly fenced measured destination CPU1")
	}
}

func TestPlacementOnlyTransfersEachCohortUIDWeightInsteadOfOptionWeight(t *testing.T) {
	settings, snapshot := placementOnlyFixture()
	second := snapshot.Options[2].Instance
	second.ID = "second-shared-physical"
	snapshot.Instances = append(snapshot.Instances, second)
	snapshot.Routing = map[string]map[string]int32{
		snapshot.Workloads[0].UID: {snapshot.Instances[0].ID: 3, second.ID: 5},
		snapshot.Workloads[1].UID: {snapshot.Instances[0].ID: 7, second.ID: 11},
	}
	third := snapshot.Nodes[1]
	third.Node = NodeReference{Name: "third", UID: "n3"}
	snapshot.Nodes = append(snapshot.Nodes, third)
	option := second
	profile := *second.Profile
	profile.ID, profile.NodeName, profile.Condition.NodeUID = "third-cpu1", third.Node.Name, third.Node.UID
	option.ID, option.Node, option.Profile = "third-slot", third.Node, &profile
	option = schedulerMeasuredInstance(option, snapshot.Workloads[0], 0)
	snapshot.Options = append(snapshot.Options, SchedulerOption{Instance: option, CompatibleUIDs: []string{snapshot.Workloads[0].UID, snapshot.Workloads[1].UID}})
	for index := range snapshot.Options {
		snapshot.Options[index].Instance.Weight = 19
	}
	candidates, _ := schedulerEnumerateForTest(t, settings, snapshot)
	moved := false
	for _, candidate := range candidates {
		if len(candidate.Instances) != 2 {
			t.Fatalf("Replicas OFF changed the physical cohort count: %+v", candidate.Instances)
		}
		for index, instance := range candidate.Instances {
			if instance.Node == third.Node {
				moved = true
			}
			sourceID := snapshot.Instances[index].ID
			for _, workload := range snapshot.Workloads {
				if candidate.Routing[workload.UID][instance.ID] != snapshot.Routing[workload.UID][sourceID] {
					t.Fatalf("Routing OFF substituted option/another replica weight for the corresponding cohort's per-UID routing: %v", candidate.Routing)
				}
			}
		}
	}
	if !moved {
		t.Fatal("nonunit routing weights prevented the ordinary measured placement move")
	}
}

func overlappingControlFixture() (SchedulerSettings, SchedulerSnapshot) {
	settings, snapshot := placementOnlyFixture()
	settings.Controls = SchedulerControls{Enabled: true, Placement: true, Resources: true, Batching: true}
	snapshot.Nodes[1].CPUs = []int{0, 1, 2, 3, 4, 5, 6, 7}
	a := snapshot.Options[3].Instance
	a.ID, a.MaxBatch, a.WorkloadUIDs = "singleton-a", 1, []string{snapshot.Workloads[0].UID}
	a = schedulerMeasuredInstance(a, snapshot.Workloads[0], 0)
	b := a
	profile := *a.Profile
	metadata := *profile.RuntimeMetadata
	profile.ID += "-singleton-b"
	profile.Condition.CPU.AffinityCPUs, metadata.CPUAffinity = []int{4, 5}, []int{4, 5}
	profile.RuntimeMetadata = &metadata
	b.ID, b.Profile, b.Allocation.CPUs, b.WorkloadUIDs = "singleton-b", &profile, []int{4, 5}, []string{snapshot.Workloads[1].UID}
	b = schedulerMeasuredInstance(b, snapshot.Workloads[1], 0)
	third := snapshot.Nodes[1]
	third.Node = NodeReference{Name: "third", UID: "n3"}
	snapshot.Nodes = append(snapshot.Nodes, third)
	target := a
	targetProfile := *a.Profile
	targetProfile.ID, targetProfile.NodeName, targetProfile.Condition.NodeUID = "third-cpu2-batch1", third.Node.Name, third.Node.UID
	target.ID, target.Node, target.Profile = "third-slot", third.Node, &targetProfile
	target = schedulerMeasuredInstance(target, snapshot.Workloads[0], 0)
	for _, instance := range []SchedulerInstance{a, b, target} {
		snapshot.Options = append(snapshot.Options, SchedulerOption{Instance: instance, CompatibleUIDs: []string{snapshot.Workloads[0].UID, snapshot.Workloads[1].UID}})
	}
	snapshot.Instances = append(snapshot.Instances, a, b)
	snapshot.Routing[snapshot.Workloads[0].UID][a.ID] = 2
	snapshot.Routing[snapshot.Workloads[1].UID][b.ID] = 5
	return settings, snapshot
}

func TestDisabledControlsAreBoundToExactOverlappingCohortNotSingletons(t *testing.T) {
	for _, disabled := range []string{"placement", "resources", "batching"} {
		t.Run(disabled, func(t *testing.T) {
			settings, snapshot := overlappingControlFixture()
			switch disabled {
			case "placement":
				settings.Controls.Placement = false
			case "resources":
				settings.Controls.Resources = false
			case "batching":
				settings.Controls.Batching = false
			}
			candidates, _ := schedulerEnumerateForTest(t, settings, snapshot)
			if len(candidates) < 2 {
				t.Fatal("exact-cohort fencing removed all legitimate independent-control alternatives")
			}
			for _, candidate := range candidates {
				shared := 0
				for _, instance := range candidate.Instances {
					if len(instance.WorkloadUIDs) != 2 {
						continue
					}
					shared++
					original := snapshot.Instances[0]
					if (disabled == "placement" && instance.Node != original.Node) ||
						(disabled == "resources" && (instance.Allocation.CPUCount != 1 || instance.Allocation.Threads != 1)) ||
						(disabled == "batching" && instance.MaxBatch != 2) {
						t.Fatalf("%s OFF borrowed a singleton's execution condition for the shared cohort: %+v", disabled, instance)
					}
				}
				if shared != 1 {
					t.Fatalf("disabled topology/replica control changed the shared physical cohort count: %d", shared)
				}
			}
		})
	}
}
