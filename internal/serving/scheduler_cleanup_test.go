package serving

import (
	"maps"
	"testing"
	"time"
)

// Explicit whole-workload fences such as reset/deletion remain exclusive.
func TestExclusiveWorkloadReservationKeepsWorkloadFixed(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod = time.Minute, 10*time.Second, time.Minute
	s.MaxReplicas, s.MaxInstances = 8, 8
	base := snap.Instances[0]
	nodes := []NodeReference{base.Node,
		poolsAddNode(&snap, "rasp5", map[string]float64{"fixed": 10}),
		poolsAddNode(&snap, "genesis3", map[string]float64{"fixed": 10}),
		poolsAddNode(&snap, "retiring", map[string]float64{"fixed": 10}),
	}
	snap.Instances, snap.Options = nil, nil
	ids := []string{"nano", "rasp5", "genesis3", "old-source"}
	reservation := SchedulerReservation{Owner: "workload-reset", Nodes: nodes, WorkloadUIDs: []string{"w1"}, Instances: ids, PodUIDs: []string{"old-pod"}}
	for k, node := range nodes {
		i := poolsInstance(base, ids[k], node, "fixed", "w1")
		i.ID, i.OwnerWorkloadUID = ids[k], "w1"
		snap.Instances = append(snap.Instances, i)
		snap.Options = append(snap.Options, poolsOption(i))
	}
	// The retiring runner is still physically Ready, but is not a destination.
	snap.Routing = map[string]map[string]int32{"w1": {ids[0]: 255, ids[1]: 663, ids[2]: 82}}
	snap.Reservations = []SchedulerReservation{reservation}
	for _, rate := range []float64{95.3, 100.2, 97.5} {
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		d := capacityDecideForTest(t, s, snap)
		if d.Workloads[0].State != schedulerWorkloadFixed || d.Workloads[0].Reason != "ReservedScope:workload-reset" {
			t.Fatalf("%.1f/s escaped the whole-workload reservation: %+v", rate, d.Workloads)
		}
		if d.Target != nil && !maps.Equal(d.Target.Routing["w1"], snap.Routing["w1"]) {
			t.Fatalf("%.1f/s changed the reserved route: %+v", rate, d.Target)
		}
	}
}
