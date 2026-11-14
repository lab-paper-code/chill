package serving

import (
	"math"
	"slices"
	"testing"
	"time"
)

// These are the physical fixtures from BatchLimitChangeIsPolicyRoute and
// SingleNodeStoppingModeRaiseNeedsSpareSupply, not the fast-only E5 harness.
func TestCapacityDeltaSlow(t *testing.T) {
	compared, excludedBridges, slowPlans := 0, 0, 0
	for _, rate := range []float64{50, 150} {
		s, snap := schedulerFixture()
		s.ComparisonPeriod = time.Minute
		s.ScaleDownHeadroomFraction = 0.2
		profile := *snap.Instances[0].Profile
		profile.Batches = append(slices.Clone(profile.Batches), schedulerTestBatchProfile(BatchProfile{Batch: 2, InputSlopeJoules: schedulerTestSlope(0.8), MemoryBytes: 100}, []float64{float64(15 * time.Millisecond)}))
		snap.Instances[0].Profile = &profile
		option := snap.Instances[0]
		option.MaxBatch = 2
		snap.Options, snap.Costs = []SchedulerOption{poolsOption(option)}, nil
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		p := capacityPlannerForTest(t, s, snap)
		count, bridges, slow := capacityDeltaAllBounded(t, p)
		compared, excludedBridges, slowPlans = compared+count, excludedBridges+bridges, slowPlans+slow
	}
	for _, rate := range []float64{5, 30} {
		s, snap := schedulerFixture()
		s.ComparisonPeriod = time.Minute
		s.ScaleDownHeadroomFraction = 0.2
		current, n1 := snap.Instances[0], snap.Instances[0].Node
		raised := poolsInstance(current, "raised", n1, schedulerTestEcoMode, "w1")
		snap.Options = append(snap.Options, poolsOption(raised))
		unloaded := snap.Baselines[0]
		unloaded.ID, unloaded.PowerMode = "greedy-eco-baseline", schedulerTestEcoMode
		snap.Baselines = append(snap.Baselines, unloaded)
		snap.Costs = []OperationCost{
			poolsCost(schedulerActionPower, n1, "fixed", schedulerTestEcoMode, "", schedulerState(current)),
			poolsCost(schedulerActionRemove, n1, schedulerState(current), "unloaded", schedulerTestEcoMode),
			poolsCost(schedulerActionPrepare, n1, "unloaded", schedulerState(raised), schedulerTestEcoMode),
		}
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		p := capacityPlannerForTest(t, s, snap)
		count, bridges, slow := capacityDeltaAllBounded(t, p)
		compared, excludedBridges, slowPlans = compared+count, excludedBridges+bridges, slowPlans+slow
	}
	if compared == 0 || slowPlans == 0 {
		t.Fatalf("missing reduction coverage compared=%d slow-plans=%d", compared, slowPlans)
	}
	t.Logf("bounded-comparisons=%d slow-plans=%d bridge-excluded=%d max-J-diff=0", compared, slowPlans, excludedBridges)
}

func capacityDeltaAllBounded(t testing.TB, p *capacityPlanner) (compared, excludedBridges, slowPlans int) {
	t.Helper()
	tab := p.planTable()
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	s := newSearchState(p, tab, choice)
	for q := range tab.index {
		if tab.eligible[q] && p.plans[tab.node[q]][tab.index[q]].reduces {
			slowPlans++
		}
	}
	var visit func(int)
	visit = func(n int) {
		if n < len(choice) {
			for _, q := range tab.byPlan[n] {
				if tab.eligible[q] {
					choice[n] = q
					visit(n + 1)
				}
			}
			return
		}
		indices := capacityDeltaChoice(tab, choice)
		if !p.bounded(indices) {
			return
		}
		bridge, reason := p.bridgeSchedule(indices)
		if bridge != nil || reason != "" {
			excludedBridges++
			return
		}
		s.reset(choice)
		for node := range choice {
			base := slices.Clone(choice)
			base[node] = tab.byPlan[node][0]
			s.reset(base)
			capacityDeltaAssert(t, p, s, node, choice[node])
			compared++
		}
	}
	visit(0)
	return compared, excludedBridges, slowPlans
}

func TestCapacityDeltaRetainedArithmetic(t *testing.T) {
	p := capacityDeltaTiny([]float64{1, 2, 3}, 2.3)
	p.settings.Controls.Routing = false
	for n, plans := range p.plans {
		plan := plans[0]
		plan.retained[0][0].weight = int32([]int{643, 357, 13}[n])
		// One instance has one power model, before and after the route.
		plan.retained[0][0].model = schedulerTestModel([]float64{0.07068, 0.07453, 0.32292}[n])
		plan.items[0][0].model = plan.retained[0][0].model
		other := plan.retained[0][0]
		other.id += "-second"
		other.weight, other.routingWeight = 29, 0.125
		plan.retained[0] = append(plan.retained[0], other)
		plan.keep = false
		plan.preMax, plan.preMean, plan.postMean = 20.5, 20.5, 80
		plan.preJ, plan.postJ, plan.mid, plan.after = 113.906, 18.092, 4.8985, 4.8866
		plan.items[0] = append(plan.items[0], other)
	}
	p.compared[1] = false
	tab := p.planTable()
	s := newSearchState(p, tab, []int{0, 1, 2})
	key, exact := capacityDeltaAssert(t, p, s, 0, 0)
	if math.Abs(key.joules-exact.joules) > 1e-9 {
		t.Fatal("retained fold or clamped Node energy differs")
	}
	nodeJ := tab.energy(0, key.route, p.periodSeconds)
	mainJ := p.plans[0][0].energy(key.route, p.periodSeconds)
	if nodeJ < 0 || nodeJ != mainJ {
		t.Fatalf("clamped node J=%.17g main J=%.17g", nodeJ, mainJ)
	}
	t.Logf("r=%g H=%g H-r<tauPost: main=delta Node J=%.17g max-J-diff=0", key.route, p.periodSeconds, nodeJ)
}

func TestCapacityEnergyExplicitRouteBeforePreparation(t *testing.T) {
	p := capacityDeltaTiny([]float64{4, 5}, 2)
	p.periodSeconds = 3
	plan := *p.plans[0][0]
	plan.keep, plan.preMean, plan.preMax, plan.postMean = false, 5, 6, 7
	plan.preJ, plan.postJ, plan.mid, plan.after = 11, 13, 17, 19
	p.plans[0] = append(p.plans[0], &plan)
	tab := p.planTable()
	q := tab.byPlan[0][1]
	for _, route := range []float64{0, 2, 6, 10} {
		if got, want := tab.energy(q, route, p.periodSeconds), plan.energy(route, p.periodSeconds); got != want || got < 0 {
			t.Fatalf("route=%g scalar energy=%g node energy=%g", route, got, want)
		}
	}
}
