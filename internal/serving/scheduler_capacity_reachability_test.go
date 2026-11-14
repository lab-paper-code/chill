package serving

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// Rebuild synthetic targets through the production plan path so retained Ready
// supply, placement identity and replay actions describe the same transition.
func capacityV2RandomPlanner(trial int, percentile float64) *capacityPlanner {
	p := capacityRandomPlanner(rand.New(rand.NewPCG(uint64(trial), 7)), 1+trial%3, 1+trial%3, percentile)
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

// TestCapacityV2HeuristicAdmitsExactFeasible asserts the property for uniformly
// spaced (or empty) arrival streams with captured initial queues whose SLO
// quantile is the cohort maximum, plus a spaced p95 stream with an initial idle
// gap. The fluid score charges each phase its last request's delay, a phase
// maximum, so it bounds only a quantile that is the maximum. The Workload API
// admits percentiles below 100 only, so random workloads use p99 and are judged
// when their cohort has fewer than 100 requests, where rank ceil(0.99n) is n.
// These fixtures exclude simultaneous future-arrival bursts; the point score
// does not generally bound their quantile.
func TestCapacityV2HeuristicAdmitsExactFeasible(t *testing.T) {
	checked := 0
	for trial := range 300 {
		p := capacityV2RandomPlanner(trial, 99)
		if trial == 0 {
			p = capacityDelayedArrivalPlanner(t)
		}
		tab := p.planTable()
		choice, flat := make([]int, len(p.plans)), make([]int, len(p.plans))
		for {
			for n, j := range choice {
				flat[n] = tab.byPlan[n][j]
			}
			s := newSearchState(p, tab, flat)
			key := s.delta(0, flat[0])
			exact := p.evaluate(slices.Clone(choice), key.route)
			if exact.exclusion == "" && exact.feasible {
				checked++
				if !key.feasible {
					for w, ws := range p.ws {
						sum := s.sums[w]
						excess, eligible := capacityHeuristic(ws, tab.arrivals[w], sum.routingRate, sum.retainedRoutingRate, sum.serviceNanos, sum.retainedServiceNanos, key.route, p.periodSeconds)
						if excess == 0 && eligible {
							continue
						}
						var quantile *float64
						population := 0
						for _, score := range exact.feedback {
							if score.UID == ws.d.UID {
								quantile, population = score.SuccessQuantileNanos, score.Population
							}
						}
						if trial > 0 && empiricalSLORank(population, ws.d.SLO.Percentile) != population {
							continue // a below-maximum quantile is outside the property
						}
						if quantile == nil {
							t.Errorf("trial=%d choice=%v w=%s exact quantile missing, fluid excess=%g eligible=%t", trial, choice, ws.d.UID, excess, eligible)
						} else {
							t.Errorf("trial=%d choice=%v route=%g w=%s target=%s exactQuantile=%g exactSlackNanos=%g fluidExcess=%g mu=%g retained=%g population=%d", trial, choice, key.route, ws.d.UID, ws.d.SLO.TargetLatency, *quantile, float64(ws.d.SLO.TargetLatency)-*quantile, excess, sum.routingRate, sum.retainedRoutingRate, population)
						}
					}
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
		p := capacityV2RandomPlanner(trial, 95)
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
