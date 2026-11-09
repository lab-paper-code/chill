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
// nodes. Duplicate reservations and a late physical slot count exactly once;
// neither an uncreated target nor an unconfirmed physical slot supplies κ.
func TestDefectEResidualPendingOccupancyKeepsReplicaAndInstanceLimits(t *testing.T) {
	const replicaBudget = "replicas"
	for _, physical := range []string{"uncreated", "late-slot", "inventory-loss"} {
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
					if physical == "late-slot" {
						actual := wanted
						actual.ID, actual.TargetID, actual.Ready = "pod/container", wanted.ID, false
						since := snap.At.Add(-s.ObservationWindow)
						actual.UnroutedSince = &since
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
					s.MinimumDemand = map[string]float64{wanted.WorkloadUID: 10}
					next := poolsInstance(wanted, "independent-slot", returned, "fixed", wanted.WorkloadUID)
					snap.Options = append(snap.Options, poolsOption(next))
					snap.Costs = append(snap.Costs, poolsCost(schedulerActionPrepare, returned, "unloaded", schedulerState(next), "fixed"))
					pending := schedulerUncreatedReservations(snap, wanted.Node)
					wantPending := 1
					if physical == "late-slot" {
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
						if plan.Node == wanted.Node && plan.Key == capacitySearchStartKeep && plan.Kappa[wanted.WorkloadUID] != 0 {
							t.Fatalf("pending occupancy supplied capacity: %+v", plan)
						}
					}
				})
			}
		}
	}
}
