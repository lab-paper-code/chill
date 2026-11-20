package serving

import (
	"fmt"
	"slices"
	"testing"
)

// T7: retained unknown occupancy is neither a free GPU nor free memory. The
// physical gate applies even when the proposed new allocation could coexist.
func TestDefectEUnknownOccupancyCannotBeReused(t *testing.T) {
	const gpuUUID = "defect-e-gpu"
	for _, allocation := range []SchedulerAllocation{{GPUUUID: gpuUUID}, {MemoryBytes: 9500}, {MemoryBytes: 1000}} {
		t.Run(fmt.Sprint(allocation), func(t *testing.T) {
			s, snap := schedulerFixture()
			snap.Nodes[0].GPUUUIDs = []string{gpuUUID}
			snap.Instances[0].Allocation.GPUUUID = gpuUUID
			snap.Instances[0].Ready = false // Native-unready without a projected reason.
			wanted := snap.Instances[0]
			wanted.ID, wanted.Ready, wanted.Allocation = "new-slot", true, allocation
			target := SchedulerCandidate{Nodes: snap.Nodes, Instances: []SchedulerInstance{snap.Instances[0], wanted}, Routing: map[string]map[string]int32{"w1": {}}}
			defectERequireNoPhysicalActions(t, schedulerPlan(s, snap, target), wanted.Node)
		})
	}
}

// T8: a partially returned owner counts only targets on its residual held
// nodes. Duplicate reservations and a late physical slot count exactly once
// against the budgets; an uncreated target supplies no κ, a physical slot its
// reservation is still preparing supplies its target's κ (committed capacity),
// which 10 req/s needs no addition to, so that case offers 1000 req/s.
func TestDefectEResidualPendingOccupancyKeepsReplicaAndInstanceLimits(t *testing.T) {
	const replicaBudget, lateSlot = "replicas", "late-slot"
	for _, physical := range []string{"uncreated", lateSlot, "inventory-loss"} {
		for _, budget := range []string{replicaBudget, "instances"} {
			for _, limit := range []int{1, 2} {
				t.Run(fmt.Sprintf("%s/%s/%d", physical, budget, limit), func(t *testing.T) {
					s, snap := schedulerFixture()
					wanted := snap.Instances[0]
					wanted.OwnerWorkloadUID = wanted.WorkloadUID
					returned := poolsAddNode(&snap, "returned", map[string]float64{"fixed": 10})
					oldTarget := poolsInstance(wanted, "returned-slot", returned, "fixed", wanted.WorkloadUID)
					goal := SchedulerCandidate{Instances: []SchedulerInstance{wanted, oldTarget}}
					snap.Instances, snap.Routing = nil, nil
					residual := SchedulerReservation{Owner: "partially-returned", Nodes: []NodeReference{wanted.Node}, Target: &goal}
					snap.Reservations = []SchedulerReservation{residual, residual}
					if physical == lateSlot {
						actual := wanted
						actual.ID, actual.TargetID, actual.Ready = "pod/container", wanted.ID, false
						snap.Instances = []SchedulerInstance{actual}
					}
					if physical == "inventory-loss" {
						snap.Nodes[0].Reasons = []string{NodePlacementInventoryMissing}
					}
					s.MaxReplicas, s.MaxInstances = 4, 4
					if budget == replicaBudget {
						s.MaxReplicas = limit
					} else {
						s.MaxInstances = limit
					}
					load := 10.0
					if physical == lateSlot {
						load = 1000
					}
					capacityLoad(s, &snap, snap.Workloads[0], load)
					next := poolsInstance(wanted, "independent-slot", returned, "fixed", wanted.WorkloadUID)
					snap.Options = append(snap.Options, poolsOption(next))
					snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, returned, "unloaded", schedulerState(next), "fixed"))
					pending := schedulerUncreatedReservations(snap, wanted.Node)
					wantPending := 1
					if physical == lateSlot {
						wantPending = 0
					}
					if len(pending) != wantPending || len(schedulerUncreatedReservations(snap, returned)) != 0 {
						t.Fatalf("residual reservation deduplication changed: %+v", pending)
					}
					d := capacityDecideForTest(t, s, snap)
					expanded := d.Target != nil && slices.ContainsFunc(d.Target.Actions, func(a Action) bool {
						return a.Kind == schedulerActionPrepare && a.Node == returned
					})
					if expanded != (limit == 2) {
						t.Fatalf("pending occupancy exceeded or double-counted %s=%d: target=%+v reasons=%v", budget, limit, d.Target, d.Reasons)
					}
					for _, plan := range d.Capacity.Plans {
						if plan.Node == wanted.Node && plan.Key == capacitySearchStartKeep && (plan.RoutingRate[wanted.WorkloadUID] != 0) != (physical == lateSlot) {
							t.Fatalf("pending occupancy supplied capacity wrongly: %+v", plan)
						}
					}
				})
			}
		}
	}
}
