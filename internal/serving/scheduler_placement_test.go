package serving

import (
	"slices"
	"testing"
	"time"
)

// placementFixture: w1 runs i1 on n1 (CPU0, batch limit 2); n2 offers the
// same CPU0 option, a two-CPU option and the batch-1 variants.
func placementFixture() (SchedulerSettings, SchedulerSnapshot, NodeReference) {
	s, snap := schedulerFixture()
	profile := *snap.Instances[0].Profile
	profile.Batches = append(slices.Clone(profile.Batches), BatchProfile{Batch: 2, DurationNanos: []float64{float64(15 * time.Millisecond)}, EnergyPerInputJoules: 1, MemoryBytes: 100})
	snap.Instances[0].Profile, snap.Instances[0].MaxBatch = &profile, 2
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	same := poolsInstance(snap.Instances[0], "same", n2, "fixed", "w1")
	wide := poolsInstance(snap.Instances[0], "wide", n2, "fixed", "w1")
	wideProfile := *wide.Profile
	wideProfile.ID += "-wide"
	wideProfile.Condition.CPU.AffinityCPUs, wideProfile.Condition.CPU.ORTIntraOpThreads = []int{0, 1}, 2
	metadata := *wideProfile.RuntimeMetadata
	metadata.CPUAffinity, metadata.IntraOpThreads = []int{0, 1}, 2
	wideProfile.RuntimeMetadata = &metadata
	wide.Profile, wide.Allocation.CPUs, wide.Allocation.Threads = &wideProfile, []int{0, 1}, 2
	snap.Options = []SchedulerOption{poolsOption(snap.Instances[0]), poolsOption(same), poolsOption(wide)}
	snap.Costs = []OperationCost{poolsCost(schedulerActionRemove, snap.Nodes[0].Node, schedulerState(snap.Instances[0]), "unloaded", "fixed")}
	for _, i := range []SchedulerInstance{same, wide} {
		snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(i), "fixed"))
	}
	return s, snap, n2
}

// D-R8-1 (m4 r8 16:53:29): two rasp5 instances of one Workload served 35–38
// req/s against their summed κ for 40 req/s. A node plan holds one instance;
// more capacity on a Node comes from a wider option.
func TestNodePlanHoldsOneInstancePerWorkload(t *testing.T) {
	s, snap := schedulerFixture()
	n2 := poolsAddNode(&snap, "n2", map[string]float64{"fixed": 10})
	cpu0 := poolsInstance(snap.Instances[0], "cpu0", n2, "fixed", "w1")
	cpu1 := poolsInstance(schedulerTestCPU(snap.Instances[0]), "cpu1", n2, "fixed", "w1")
	snap.Options = append(snap.Options, poolsOption(cpu0), poolsOption(cpu1))
	for _, i := range []SchedulerInstance{cpu0, cpu1} {
		snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, n2, "unloaded", schedulerState(i), "fixed"))
	}
	p := capacityPlannerForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	n := slices.IndexFunc(p.snapshot.Nodes, func(node SchedulerNode) bool { return node.Node == n2 })
	single := 0
	for _, plan := range p.plans[n] {
		if len(plan.instances) > 1 {
			t.Fatalf("node plan with two instances of one Workload: %+v", plan.record)
		}
		if plan.record.Eligible && len(plan.instances) == 1 {
			single++
		}
	}
	if single != 2 {
		t.Fatalf("each option alone must stay a node plan, got %d", single)
	}
}

// The disabled controls gate node plans: placement OFF keeps each Workload on
// its Nodes, resources OFF keeps its CPU/GPU choice, batching OFF its batch
// limit, and replicas OFF its total instance count while it moves.
func TestDisabledControlsGateNodePlans(t *testing.T) {
	for _, disabled := range []string{"placement", "resources", "batching", "replicas"} {
		t.Run(disabled, func(t *testing.T) {
			s, snap, n2 := placementFixture()
			switch disabled {
			case "placement":
				s.Controls.Placement = false
			case "resources":
				s.Controls.Resources = false
			case "batching":
				s.Controls.Batching = false
			case "replicas":
				s.Controls.Replicas = false
			}
			rows := capacityRowsForTest(s, snap, nil)
			d := capacityDecideForTest(t, s, snap, rows)
			plans := poolsPlans(d, n2)
			has := func(prefix string) bool {
				return slices.ContainsFunc(plans, func(p SchedulerNodePlan) bool {
					return p.Eligible && len(p.Key) >= len(prefix) && p.Key[:len(prefix)] == prefix
				})
			}
			wide, same1, same2 := has("profile@n2-fixed-wide/"), has("profile@n2-fixed/b1/"), has("profile@n2-fixed/b2/")
			switch disabled {
			case "placement":
				if wide || same1 || same2 {
					t.Fatalf("placement OFF planned another Node: %+v", plans)
				}
			case "resources":
				if wide || !same2 {
					t.Fatalf("resources OFF changed the CPU choice: %+v", plans)
				}
			case "batching":
				if same1 || !same2 {
					t.Fatalf("batching OFF changed the batch limit: %+v", plans)
				}
			case "replicas":
				// Moving i1 to n2 keeps one instance; adding n2 beside i1 does not.
				p := capacityPlannerForTest(t, s, snap, rows)
				moved := capacityChoiceForTest(t, p, map[string]string{"n1": "", "n2": "profile@n2-fixed/b2/w1"})
				if !p.bounded(moved) {
					t.Fatal("replicas OFF rejected a move that keeps the instance count")
				}
				if added := append([]int{0}, moved[1:]...); p.bounded(added) {
					t.Fatal("replicas OFF accepted an added instance")
				}
			}
		})
	}
}
