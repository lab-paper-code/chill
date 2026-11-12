package serving

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// Rebuild synthetic targets through the production plan path so retained Ready
// supply, placement identity and replay actions describe the same transition.
func capacityV2RandomPlanner(trial int) *capacityPlanner {
	p := capacityRandomPlanner(rand.New(rand.NewPCG(uint64(trial), 7)), 1+trial%3, 1+trial%3)
	p.settings.PlanSearchStrategy = "v2"
	p.resolveCapacity()
	for n, plans := range p.plans {
		current := plans[0].instances
		p.annotate(plans[0], nil)
		p.sum(plans[0])
		rebuilt := []*capacityPlan{plans[0]}
		for _, plan := range plans[1:] {
			chosen := []capacityChoice{}
			for _, i := range plan.instances {
				w := p.index[i.WorkloadUID]
				c := capacityChoice{option: poolsOption(i), w: w}
				c.routingWeight, c.throughput, c.serviceNanos, c.reason = p.profileTerms(i, p.ws[w].d)
				chosen = append(chosen, c)
			}
			if next := p.plan(n, p.snapshot.Nodes[n].PowerMode, current, chosen); next != nil {
				// Retain the random energy problem while production derives transitions.
				next.before, next.mid, next.after = plan.before, plan.mid, plan.after
				next.preJ, next.postJ = plan.preJ, plan.postJ
				rebuilt = append(rebuilt, next)
			}
		}
		p.plans[n] = rebuilt
	}
	return p
}
func TestCapacityV2HeuristicAdmitsExactFeasible(t *testing.T) {
	checked := 0
	for trial := range 300 {
		p := capacityV2RandomPlanner(trial)
		tab := p.planTable()
		choice, flat := make([]int, len(p.plans)), make([]int, len(p.plans))
		for {
			for n, j := range choice {
				flat[n] = tab.byPlan[n][j]
			}
			s := newSearchState(p, tab, flat)
			key := s.delta(0, flat[0])
			exact := p.evaluateAtRoute(slices.Clone(choice), key.route, nil)
			if exact.exclusion == "" && exact.feasible {
				checked++
				if !key.feasible {
					for w, ws := range p.ws {
						t.Logf("w=%s target=%s percentile=%g rate=%g queue=%d mu=%g retained=%g service=%g exactQuantile=%g", ws.d.UID, ws.d.SLO.TargetLatency, ws.d.SLO.Percentile, ws.rate, ws.queued, s.sums[w].routingRate, s.sums[w].retainedRoutingRate, s.sums[w].serviceNanos, *exact.feedback[w].SuccessQuantileNanos)
					}
					t.Errorf("trial=%d choice=%v route=%g fluid excess=%v exact feedback=%+v workload=%+v sums=%+v", trial, choice, key.route, key.excesses, exact.feedback, p.ws, s.sums)
					return
				}
			}
			n := 0
			for ; n < len(choice); n++ {
				choice[n]++
				if choice[n] < len(p.plans[n]) {
					break
				}
				choice[n] = 0
			}
			if n == len(choice) {
				break
			}
		}
	}
	if checked == 0 {
		t.Fatal("no exact-feasible choices exercised")
	}
}

func TestCapacitySearchV2MatchesBruteForce(t *testing.T) {
	for trial := range 300 {
		p := capacityV2RandomPlanner(trial)
		best, err := p.search(time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		exact := capacityBruteForce(p)
		if best.feasible != (exact != nil) || exact != nil && (best.key != exact.key || best.joules != exact.joules) {
			t.Fatalf("trial %d: search feasible=%v choice=%v key=%s J=%g, brute force %+v", trial, best.feasible, best.choice, best.key, best.joules, exact)
		}
	}
}
