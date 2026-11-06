package serving

import (
	"slices"
	"testing"
	"time"
)

// These are the physical fixtures from BatchLimitChangeIsPolicyRoute and
// SingleNodeStoppingModeRaiseNeedsSpareSupply, not the fast-only E5 harness.
func TestCapacityDeltaSlow(t *testing.T) {
	compared, excludedBridges, slowPlans, blockedPlans := 0, 0, 0, 0
	for _, rate := range []float64{50, 150} {
		for _, allowed := range []bool{true, false} {
			for _, growing := range []bool{true, false} {
				s, snap := schedulerFixture()
				s.ScaleDownStabilization, s.ComparisonPeriod = 300*time.Second, time.Minute
				s.ScaleDownHeadroomFraction = 0.2
				profile := *snap.Instances[0].Profile
				profile.Batches = append(slices.Clone(profile.Batches), BatchProfile{Batch: 2, DurationNanos: []float64{float64(15 * time.Millisecond)}, EnergyPerInputJoules: 0.8, MemoryBytes: 100})
				snap.Instances[0].Profile = &profile
				option := snap.Instances[0]
				option.MaxBatch = 2
				snap.Options, snap.Costs = []SchedulerOption{poolsOption(option)}, nil
				snap.ScaleDown = map[string]SchedulerDownState{"w1": {Allowed: allowed, Supply: 100, WindowMax: 180}}
				capacityLoad(s, &snap, snap.Workloads[0], rate)
				p := capacityPlannerForTest(t, s, snap, capacityRowsForTest(s, snap, nil))
				p.ws[0].growing = growing
				count, bridges, slow, blocked := capacityDeltaAllBounded(t, p)
				compared, excludedBridges, slowPlans, blockedPlans = compared+count, excludedBridges+bridges, slowPlans+slow, blockedPlans+blocked
			}
		}
	}
	for _, rate := range []float64{5, 30} {
		s, snap := schedulerFixture()
		s.ScaleDownStabilization, s.ComparisonPeriod = time.Minute, time.Minute
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
		snap.ScaleDown = map[string]SchedulerDownState{"w1": {Supply: 10, WindowMax: 45}}
		capacityLoad(s, &snap, snap.Workloads[0], rate)
		p := capacityPlannerForTest(t, s, snap, capacityRowsForTest(s, snap, capacityKappaByProfile(map[string]float64{current.Profile.ID: 10, raised.Profile.ID: 50})))
		count, bridges, slow, blocked := capacityDeltaAllBounded(t, p)
		compared, excludedBridges, slowPlans, blockedPlans = compared+count, excludedBridges+bridges, slowPlans+slow, blockedPlans+blocked
	}
	if compared == 0 || slowPlans == 0 || blockedPlans == 0 {
		t.Fatalf("missing slow coverage compared=%d slow-plans=%d blocked=%d", compared, slowPlans, blockedPlans)
	}
	t.Logf("bounded-comparisons=%d slow-plans=%d plan-local-blocked=%d bridge-excluded=%d max-J-diff=0", compared, slowPlans, blockedPlans, excludedBridges)
}

func capacityDeltaAllBounded(t testing.TB, p *capacityPlanner) (compared, excludedBridges, slowPlans, blockedPlans int) {
	t.Helper()
	tab := p.planTable()
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	s := newSearchState(p, tab, choice)
	for q := range tab.index {
		if tab.eligible[q] && tab.slow[q] {
			slowPlans++
		}
		if tab.eligible[q] && tab.dominated[q] == "" && !tab.searchable[q] {
			blockedPlans++
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
		exact := p.evaluate(indices, p.routeOf(indices))
		if exact.bridge != nil {
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
	return compared, excludedBridges, slowPlans, blockedPlans
}

func TestCapacityDeltaRetainedArithmetic(t *testing.T) {
	p := capacityDeltaTiny([]float64{1, 2, 3}, 2.3)
	for n, plans := range p.plans {
		plan := plans[0]
		plan.retained[0][0].weight = int32([]int{643, 357, 13}[n])
		plan.retained[0][0].e = []float64{0.07068, 0.07453, 0.32292}[n]
		other := plan.retained[0][0]
		other.id += "-second"
		other.weight, other.kappa = 29, 0.125
		plan.retained[0] = append(plan.retained[0], other)
		plan.keep = false
		plan.preMax, plan.preMean, plan.postMean = 19.256, 20.5, 80
		plan.preJ, plan.postJ, plan.mid, plan.after = 113.906, 18.092, 4.8985, 4.8866
		plan.items[0] = append(plan.items[0], other)
	}
	p.compared[1] = false
	tab := p.planTable()
	s := newSearchState(p, tab, []int{0, 1, 2})
	key, exact := capacityDeltaAssert(t, p, s, 0, 0)
	if key.joules != exact.joules {
		t.Fatal("retained fold or unclamped Node energy differs")
	}
	nodeJ := tab.energy(0, key.route, p.period)
	proposalJ := tab.joules[0] + tab.mid[0]*max(0, min(key.route, p.period)-tab.tauPre[0]) +
		tab.after[0]*max(0, p.period-key.route-tab.tauPost[0])
	if nodeJ >= 0 || proposalJ <= 0 {
		t.Fatal("fixture must expose the proposal's duration-clamp mismatch")
	}
	t.Logf("r=%g H=%g preJ=%g postJ=%g mid=%g after=%g tauPre=%g tauPost=%g: v1=delta Node J=%.17g proposal-clamped=%.17g",
		key.route, p.period, tab.preJ[0], tab.postJ[0], tab.mid[0], tab.after[0], tab.tauPre[0], tab.tauPost[0], nodeJ, proposalJ)
}
