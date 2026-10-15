package serving

import (
	"slices"
	"testing"
	"time"
)

const schedulerLogicalTestNewProfile = "new-profile"

func schedulerLogicalKeyFixture() (SchedulerSnapshot, SchedulerCandidate) {
	_, snapshot := schedulerFixture()
	definition := *snapshot.Workloads[0]
	definition.UID = "second-workload"
	snapshot.Workloads = append(snapshot.Workloads, &definition)
	first := snapshot.Instances[0]
	first.ID, first.TargetID = "physical-first", "slot-first"
	first.WorkloadUIDs = []string{snapshot.Workloads[0].UID, definition.UID}
	first.Allocation.CPUs = []int{0, 1}
	first.Allocation.Threads = 2
	second := first
	second.ID, second.TargetID = "physical-second", "slot-second"
	node := snapshot.Nodes[0]
	node.Node.UID, node.Node.Name = "second-node-uid", "second-node"
	snapshot.Nodes = append(snapshot.Nodes, node)
	second.Node = node.Node
	profile := *first.Profile
	profile.ID = "second-profile"
	profile.Condition.NodeUID = node.Node.UID
	second.Profile = &profile
	snapshot.Instances = []SchedulerInstance{first, second}
	snapshot.Options = nil
	for _, instance := range snapshot.Instances {
		template := instance
		template.ID, template.TargetID = instance.TargetID, ""
		snapshot.Options = append(snapshot.Options, SchedulerOption{Instance: template})
	}
	candidate := SchedulerCandidate{Nodes: slices.Clone(snapshot.Nodes), Instances: slices.Clone(snapshot.Instances), Routing: map[string]map[string]int32{}}
	for _, workload := range snapshot.Workloads {
		candidate.Routing[workload.UID] = map[string]int32{first.ID: 1, second.ID: 2}
	}
	return snapshot, candidate
}

func schedulerMustLogicalKey(t *testing.T, snapshot SchedulerSnapshot, candidate SchedulerCandidate) string {
	t.Helper()
	key, err := SchedulerLogicalConfigKey(snapshot, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSchedulerLogicalConfigKeyPreservesExecutorReplacementAndOrdering(t *testing.T) {
	snapshot, candidate := schedulerLogicalKeyFixture()
	want := schedulerMustLogicalKey(t, snapshot, candidate)
	candidate.ID, candidate.Keep = "different-decision", true
	candidate.Instances[0].ID = "replacement-pod/replacement-container"
	candidate.Instances[0].ObservedAt = time.Unix(999, 0)
	candidate.Instances[0].CompletionOverhead += time.Second
	candidate.Instances[0].CompletionObserved = false
	candidate.Instances[0].EnvelopeSource = "new observation"
	candidate.Instances[0].Ready = false
	for _, route := range candidate.Routing {
		route[candidate.Instances[0].ID] = route["physical-first"]
		delete(route, "physical-first")
	}
	slices.Reverse(candidate.Nodes)
	slices.Reverse(candidate.Instances)
	slices.Reverse(snapshot.Workloads)
	for index := range candidate.Instances {
		candidate.Instances[index].WorkloadUIDs = slices.Clone(candidate.Instances[index].WorkloadUIDs)
		slices.Reverse(candidate.Instances[index].WorkloadUIDs)
		candidate.Instances[index].Allocation.CPUs = slices.Clone(candidate.Instances[index].Allocation.CPUs)
		slices.Reverse(candidate.Instances[index].Allocation.CPUs)
	}
	if got := schedulerMustLogicalKey(t, snapshot, candidate); got != want {
		t.Fatal("physical replacement, observations or input ordering changed logical configuration")
	}
}

func TestSchedulerLogicalConfigKeyTracksControlSemantics(t *testing.T) {
	changes := map[string]func(*SchedulerSnapshot, *SchedulerCandidate){
		"power":      func(_ *SchedulerSnapshot, c *SchedulerCandidate) { c.Nodes[0].PowerMode = "other-mode" },
		"membership": func(_ *SchedulerSnapshot, c *SchedulerCandidate) { c.Nodes[0].Pool = "other-pool" },
		"profile": func(_ *SchedulerSnapshot, c *SchedulerCandidate) {
			p := *c.Instances[0].Profile
			p.ID = schedulerLogicalTestNewProfile
			c.Instances[0].Profile = &p
		},
		"allocation": func(_ *SchedulerSnapshot, c *SchedulerCandidate) { c.Instances[0].Allocation.MemoryBytes++ },
		"routing":    func(s *SchedulerSnapshot, c *SchedulerCandidate) { c.Routing[s.Workloads[0].UID][c.Instances[0].ID]++ },
		"definition": func(s *SchedulerSnapshot, _ *SchedulerCandidate) { s.Workloads[0].Generation++ },
		"model":      func(s *SchedulerSnapshot, _ *SchedulerCandidate) { s.Workloads[0].Model.Generation++ },
		"slo":        func(s *SchedulerSnapshot, _ *SchedulerCandidate) { s.Workloads[0].SLO.TargetLatency += time.Second },
		"batch":      func(_ *SchedulerSnapshot, c *SchedulerCandidate) { c.Instances[0].MaxBatch++ },
		"wait":       func(_ *SchedulerSnapshot, c *SchedulerCandidate) { c.Instances[0].MaxWait += time.Millisecond },
		"replicas": func(_ *SchedulerSnapshot, c *SchedulerCandidate) {
			c.Instances = c.Instances[:1]
			for _, route := range c.Routing {
				delete(route, "physical-second")
			}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			snapshot, candidate := schedulerLogicalKeyFixture()
			before := schedulerMustLogicalKey(t, snapshot, candidate)
			change(&snapshot, &candidate)
			if after := schedulerMustLogicalKey(t, snapshot, candidate); after == before {
				t.Fatal("changed control semantics retained the old logical key")
			}
		})
	}
}

func TestSchedulerLogicalConfigKeyIgnoresTemplateNames(t *testing.T) {
	snapshot, candidate := schedulerLogicalKeyFixture()
	want := schedulerMustLogicalKey(t, snapshot, candidate)
	snapshot.Instances = nil
	snapshot.Options = nil
	for index := range candidate.Instances {
		candidate.Instances[index].TargetID = "renamed-template"
	}
	if got := schedulerMustLogicalKey(t, snapshot, candidate); got != want {
		t.Fatal("template names without changed cost semantics changed the key")
	}
}

func TestSchedulerLogicalConfigKeyRejectsUnknownRoutingAndRetainsReplicaMultiplicity(t *testing.T) {
	snapshot, candidate := schedulerLogicalKeyFixture()
	candidate.Routing[snapshot.Workloads[0].UID]["unknown-physical-target"] = 0
	if _, err := SchedulerLogicalConfigKey(snapshot, candidate); err == nil {
		t.Fatal("unknown route target silently disappeared from the key")
	}
	delete(candidate.Routing[snapshot.Workloads[0].UID], "unknown-physical-target")
	before := schedulerMustLogicalKey(t, snapshot, candidate)
	replica := candidate.Instances[0]
	replica.ID = "different-physical-replica"
	candidate.Instances = append(candidate.Instances, replica)
	if after := schedulerMustLogicalKey(t, snapshot, candidate); after == before {
		t.Fatal("added replica disappeared from the logical configuration")
	}
}

func TestSchedulerLogicalConfigKeyRetainsJointReplicaRouting(t *testing.T) {
	snapshot, candidate := schedulerLogicalKeyFixture()
	first := candidate.Instances[0]
	second := first
	second.ID = "second-equal-condition-replica"
	candidate.Instances = []SchedulerInstance{first, second}
	firstUID, secondUID := snapshot.Workloads[0].UID, snapshot.Workloads[1].UID
	candidate.Routing = map[string]map[string]int32{
		firstUID:  {first.ID: 1, second.ID: 2},
		secondUID: {first.ID: 1, second.ID: 2},
	}
	correlated := schedulerMustLogicalKey(t, snapshot, candidate)
	// Each workload still has weights {1,2}, but the joint replica loads change
	// from (2,4) to (3,3). Independent per-workload multisets lose that fact.
	candidate.Routing[secondUID][first.ID] = 2
	candidate.Routing[secondUID][second.ID] = 1
	balanced := schedulerMustLogicalKey(t, snapshot, candidate)
	if balanced == correlated {
		t.Fatal("joint sharing/routing correlation disappeared from the key")
	}
	candidate.Instances[0].ID, candidate.Instances[1].ID = "replacement-a", "replacement-b"
	for _, routing := range candidate.Routing {
		routing["replacement-a"], routing["replacement-b"] = routing[first.ID], routing[second.ID]
		delete(routing, first.ID)
		delete(routing, second.ID)
	}
	slices.Reverse(candidate.Instances)
	if permuted := schedulerMustLogicalKey(t, snapshot, candidate); permuted != balanced {
		t.Fatal("physical replica replacement/permutation changed joint routing semantics")
	}
}
