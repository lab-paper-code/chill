package serving

import (
	"maps"
	"testing"
	"time"
)

// A completed handoff does not release the retirement owner's planning scope.
func TestCleanupReservationKeepsWorkloadFixed(t *testing.T) {
	s, snap := schedulerFixture()
	s.ObservationWindow, s.ControlInterval, s.ComparisonPeriod, s.MaxObservationAge = time.Minute, 10*time.Second, time.Minute, time.Hour
	s.MaxReplicas, s.MaxInstances = 8, 8
	base := snap.Instances[0]
	nodes := []NodeReference{base.Node,
		poolsAddNode(&snap, "rasp5", map[string]float64{"fixed": 10}),
		poolsAddNode(&snap, "genesis3", map[string]float64{"fixed": 10}),
		poolsAddNode(&snap, "retiring", map[string]float64{"fixed": 10}),
	}
	snap.Instances, snap.Options = nil, nil
	ids := []string{"nano", "rasp5", "genesis3", "old-source"}
	kappas := []float64{10.19099904088407, 26.49779192329125, 68.45308563137857, 1000}
	byProfile := map[string]float64{}
	reservation := SchedulerReservation{Owner: "retirement", Nodes: nodes, WorkloadUIDs: []string{"w1"}, Instances: ids, PodUIDs: []string{"old-pod"}}
	for k, node := range nodes {
		i := poolsInstance(base, ids[k], node, "fixed", "w1")
		i.ID, i.OwnerWorkloadUID = ids[k], "w1"
		snap.Instances = append(snap.Instances, i)
		snap.Options = append(snap.Options, poolsOption(i))
		byProfile[i.Profile.ID] = kappas[k]
	}
	// The retiring runner is still physically Ready, but is not a destination.
	snap.Routing = map[string]map[string]int32{"w1": {ids[0]: 255, ids[1]: 663, ids[2]: 82}}
	snap.Reservations = []SchedulerReservation{reservation}
	rows := capacityRowsForTest(s, snap, capacityKappaByProfile(byProfile))
	for _, rate := range []float64{95.3, 100.2, 97.5} {
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		d := capacityDecideForTest(t, s, snap, rows)
		if d.Workloads[0].State != schedulerWorkloadFixed || d.Workloads[0].Reason != "ReservedScope:retirement" {
			t.Fatalf("%.1f/s escaped the retirement reservation: %+v", rate, d.Workloads)
		}
		if d.Target != nil && !maps.Equal(d.Target.Routing["w1"], snap.Routing["w1"]) {
			t.Fatalf("%.1f/s changed the reserved route: %+v", rate, d.Target)
		}
	}
}
