package serving

import "testing"

func TestPendingPrepareRespectsReplicaBudget(t *testing.T) {
	for _, created := range []bool{false, true} {
		for _, limit := range []int{1, 2} {
			s, snap := schedulerFixture()
			wanted := snap.Instances[0]
			goal := SchedulerCandidate{ID: "pending", Instances: []SchedulerInstance{wanted}, Routing: map[string]map[string]int32{wanted.WorkloadUID: {wanted.ID: 1000}}}
			snap.Instances, snap.Routing = nil, nil
			reservation := SchedulerReservation{Owner: "pending", Nodes: []NodeReference{wanted.Node}, Target: &goal}
			snap.Reservations = []SchedulerReservation{reservation, reservation}
			if created {
				actual := wanted
				actual.ID, actual.TargetID, actual.Ready, actual.OwnerWorkloadUID = "pending-physical", wanted.ID, false, wanted.WorkloadUID
				snap.Instances = []SchedulerInstance{actual}
			}
			s.MaxReplicas, s.MaxInstances = limit, 4
			s.MinimumDemand = map[string]float64{wanted.WorkloadUID: 10}
			node := poolsAddNode(&snap, "independent", map[string]float64{"fixed": 10})
			next := poolsInstance(wanted, "independent-slot", node, "fixed", wanted.WorkloadUID)
			snap.Options = append(snap.Options, poolsOption(next))
			snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, node, "unloaded", schedulerState(next), "fixed"))
			d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
			expanded := d.Target != nil && poolsOn(*d.Target, node)
			if expanded != (limit == 2) {
				t.Errorf("created=%v maxReplicas=%d pending=1 secondPrepare=%v", created, limit, expanded)
			}
			for _, plan := range d.Capacity.Plans {
				if plan.Node == wanted.Node && plan.Key == "keep" && plan.Kappa[wanted.WorkloadUID] != 0 {
					t.Fatalf("pending replica fabricated capacity: %+v", plan)
				}
			}
		}
	}
}

func TestPendingInventoryLossKeepsBudget(t *testing.T) {
	s, snap := schedulerFixture()
	wanted := snap.Instances[0]
	goal := SchedulerCandidate{ID: "pending", Instances: []SchedulerInstance{wanted}, Routing: map[string]map[string]int32{wanted.WorkloadUID: {wanted.ID: 1000}}}
	snap.Instances, snap.Routing = nil, nil
	snap.Reservations = []SchedulerReservation{{Owner: "pending", Nodes: []NodeReference{wanted.Node}, Target: &goal}}
	for n := range snap.Nodes {
		if snap.Nodes[n].Node == wanted.Node {
			snap.Nodes[n].Reasons = append(snap.Nodes[n].Reasons, NodePlacementInventoryMissing)
		}
	}
	s.MaxReplicas, s.MaxInstances = 1, 1
	s.Controls.Replicas = false
	s.MinimumDemand = map[string]float64{wanted.WorkloadUID: 10}
	node := poolsAddNode(&snap, "independent", map[string]float64{"fixed": 10})
	snap.Nodes[len(snap.Nodes)-1].Reasons = nil
	next := poolsInstance(wanted, "independent-slot", node, "fixed", wanted.WorkloadUID)
	snap.Options = append(snap.Options, poolsOption(next))
	snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, node, "unloaded", schedulerState(next), "fixed"))
	d := capacityDecideForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
	if d.Target != nil && poolsOn(*d.Target, node) {
		t.Fatal("missing placement inventory made the pending reservation disappear from both budgets")
	}
}
