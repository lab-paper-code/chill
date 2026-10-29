package serving

import (
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"
)

// full-E5-5-02: final route 255:663:82 at 32.5833/s, then recent demand
// 95.3/100.2/97.5/s during cleanup. Retiring capacity must not rejoin routing.
func TestCleanupReservationAllowsOnlyReadyRouteRebalancing(t *testing.T) {
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
	capacityLoad(s, &snap, snap.Workloads[0], 95.3)
	before := capacityDecideForTest(t, s, snap, rows)
	if before.Workloads[0].State != schedulerWorkloadFixed {
		t.Fatal("pre-handoff scope must remain Fixed")
	}
	for _, rate := range []float64{95.3, 100.2, 97.5} {
		snap.Reservations[0].RouteOnly = map[string][]string{"w1": ids[:3]}
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		d := capacityDecideForTest(t, s, snap, rows)
		if d.Workloads[0].State != WorkloadControlled || d.Target == nil || d.Target.Keep || maps.Equal(d.Target.Routing["w1"], snap.Routing["w1"]) {
			t.Fatalf("%.1f/s did not rebalance during cleanup: %+v", rate, d)
		}
		if d.Target.Routing["w1"]["old-source"] != 0 {
			t.Fatal("retiring source supplied route capacity")
		}
		if len(d.Target.Actions) != 1 || d.Target.Actions[0].Kind != schedulerActionRoute {
			t.Fatalf("cleanup introduced lifecycle actions: %+v", d.Target.Actions)
		}
		p := capacityPlannerForTest(t, s, snap, rows)
		for n, plans := range p.plans {
			for _, plan := range plans[1:] {
				if plan.record.Eligible {
					t.Fatalf("reserved Node %s allowed physical/power choice", p.snapshot.Nodes[n].Node.Name)
				}
			}
		}
		// A free Node must not admit a new preparation for the route-only UID.
		free := poolsAddNode(&snap, fmt.Sprintf("free-%.1f", rate), map[string]float64{"fixed": 10})
		candidate := poolsInstance(base, "new-prepare", free, "fixed", "w1")
		if p.controlAllows(0, candidate, nil) {
			t.Fatal("route-only Workload admitted a new preparation")
		}
		if !slices.Equal(snap.Reservations[0].PodUIDs, []string{"old-pod"}) {
			t.Fatal("routing removed retirement responsibility")
		}
	}
	// The documented high-backlog distribution is the unchanged κ allocator,
	// not a special cleanup weighting rule.
	items := []capacityItem{{id: ids[0], kappa: kappas[0]}, {id: ids[1], kappa: kappas[1]}, {id: ids[2], kappa: kappas[2]}}
	weights, _, _ := capacityWeights(96.03333333333335, 20, items)
	if !slices.Equal(weights, []int32{97, 252, 651}) {
		t.Fatalf("recorded high-backlog route changed: %v", weights)
	}
}
