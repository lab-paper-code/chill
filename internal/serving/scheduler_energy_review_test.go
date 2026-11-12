package serving

import (
	"testing"
	"time"
)

func TestCapacitySharedUnknownPreparePreservesOnlyDurationIndependentSaving(t *testing.T) {
	for _, targetWatts := range []float64{0, 5} {
		t.Run(map[float64]string{0: "constant-zero", 5: "duration-dependent-post"}[targetWatts], func(t *testing.T) {
			settings, snapshot := schedulerFixture()
			settings.ComparisonPeriod = 600 * time.Second
			settings.MinimumDemand = map[string]float64{"w1": 150}
			recoveryNode := poolsAddNode(&snapshot, "recovery", map[string]float64{"fixed": 10})
			recovery := poolsInstance(snapshot.Instances[0], "recovery", recoveryNode, "fixed", "w1")
			snapshot.Options = append(snapshot.Options, poolsOption(recovery))
			snapshot.Costs = append(snapshot.Costs, poolsCost(schedulerActionPrepare, recoveryNode, "unloaded", "another-profile", "fixed"))
			savingNode := poolsAddNode(&snapshot, "saving", map[string]float64{"fixed": 10, "eco": targetWatts})
			power := poolsCost(schedulerActionPower, savingNode, "fixed", "eco", "")
			power.Trials = []OperationTrial{{Duration: time.Second, Joules: 10}}
			snapshot.Costs = append(snapshot.Costs, power)
			capacityLoad(settings, &snapshot, snapshot.Workloads[0], 150)
			planner := capacityPlannerForTest(t, settings, snapshot)
			choice := capacityChoiceForTest(t, planner, map[string]string{recoveryNode.UID: planKey([]SchedulerInstance{recovery}), savingNode.UID: ""})
			best := planner.evaluate(choice, planner.routeOf(choice))
			if !best.feasible {
				t.Fatalf("recovery fixture must meet SLO: %+v", best)
			}
			paid := planner.payback(best)
			for n, node := range planner.snapshot.Nodes {
				// The empty-node power action is after the route. A nonzero
				// target idle leaves max(0,H-route-post) dependent on the
				// shared unknown prepare when it crosses the horizon.
				if node.Node == savingNode && (paid.choice[n] == choice[n]) != (targetWatts == 0) {
					t.Fatalf("duration-independent saving retained=%t, target idle=%g unpaid=%v", paid.choice[n] == choice[n], targetWatts, paid.unpaid)
				}
			}
		})
	}
}
