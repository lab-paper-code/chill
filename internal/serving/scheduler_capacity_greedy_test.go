package serving

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func capacityDeltaChoice(tab *planTable, choice []int) []int {
	result := make([]int, len(choice))
	for n, q := range choice {
		result[n] = tab.index[q]
	}
	return result
}

func capacityDeltaSaved(key protocol.Rank) protocol.Rank {
	result := protocol.Rank{Excesses: make([]float64, len(key.Excesses)), Choice: make([]int, len(key.Choice))}
	key.CopyInto(&result)
	return result
}

func capacityDeltaKey(key orderKey) string {
	var text strings.Builder
	for _, q := range key.choice {
		text.WriteString(key.table.keyParts[q])
	}
	return text.String()
}

// This oracle checks only energy, physical bounds, route timing, and keys.
// Frozen fluid excesses deliberately do not equal finite-demand replay latency.
func capacityDeltaAssert(t testing.TB, p *capacityPlanner, s *searchState, n, q int) (orderKey, capacityResult) {
	t.Helper()
	key := s.delta(n, q)
	choice := capacityDeltaChoice(s.table, key.choice)
	energy := p.evaluateEnergyAtWindow(choice, p.routeOf(choice), nil, p.period)
	physicalKeep := true
	for node, j := range choice {
		physicalKeep = physicalKeep && p.plans[node][j].keep
	}
	if key.keep != physicalKeep || key.route != energy.route || capacityDeltaKey(key) != energy.key || key.bounded != p.bounded(choice) {
		t.Fatalf("move=(%d,%d) key=%+v energy=%+v", n, q, key, energy)
	}
	// Integer routing and continuous fluid routing have identical request energy
	// only when their shares agree, or when routing control is disabled.
	equalShares := true
	if p.settings.Controls.Routing {
		for w := range p.ws {
			total := 0.0
			for node, j := range choice {
				for _, item := range p.plans[node][j].items[w] {
					total += float64(item.routingWeight)
				}
			}
			if total == 0 {
				continue
			}
			for node, j := range choice {
				for _, item := range p.plans[node][j].items[w] {
					if math.Abs(float64(energy.weights[w][item.id])/schedulerRouteUnits-float64(item.routingWeight)/total) > 1e-14 {
						equalShares = false
					}
				}
			}
		}
	}
	if equalShares && math.Abs(key.joules-energy.joules) > 1e-9*max(1, math.Abs(energy.joules)) {
		t.Fatalf("energy equation differs: scalar=%.17g oracle=%.17g", key.joules, energy.joules)
	}
	return key, energy
}

func TestCapacityDeltaFrozenWalk(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261107, 3))
	for _, space := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}} {
		for _, rates := range e5Mixes {
			p := e5Planner(rates, evaluationScaled(space.variants, space.modes), e5Start)
			for _, routing := range []bool{true, false} {
				p.settings.Controls.Routing = routing
				capacityDeltaWalk(t, p, rng)
			}
		}
	}
}

func capacityDeltaWalk(t testing.TB, p *capacityPlanner, rng *rand.Rand) {
	t.Helper()
	tab := p.planTable()
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	s, fresh := newSearchState(p, tab, choice), newSearchState(p, tab, choice)
	eligible := make([][]int, len(choice))
	for n, plans := range tab.byPlan {
		for _, q := range plans {
			if tab.searchable[q] {
				eligible[n] = append(eligible[n], q)
			}
		}
	}
	for sample := range 96 {
		if sample%8 == 0 {
			for n := range choice {
				choice[n] = eligible[n][rng.IntN(len(eligible[n]))]
			}
			s.reset(choice)
		}
		n := rng.IntN(len(choice))
		q := eligible[n][rng.IntN(len(eligible[n]))]
		before, sums, times := slices.Clone(s.choice), slices.Clone(s.sums), slices.Clone(s.tPre)
		total, route := s.total, s.r
		key, _ := capacityDeltaAssert(t, p, s, n, q)
		moved := slices.Clone(key.choice) // Delta's scratch is borrowed.
		if !slices.Equal(before, s.choice) || !slices.Equal(sums, s.sums) || !slices.Equal(times, s.tPre) || total != s.total || route != s.r {
			t.Fatal("Delta changed committed state")
		}
		s.apply(n, q)
		fresh.reset(moved)
		if !reflect.DeepEqual(s.sums, fresh.sums) || s.total != fresh.total || s.r != fresh.r || !slices.Equal(s.tPre, fresh.tPre) || !slices.Equal(s.choice, fresh.choice) {
			t.Fatalf("apply drift sample=%d", sample)
		}
	}
}

// Tiny fixtures are scalar/energy-only; callers must not run demand replay.
func capacityDeltaTiny(rates []float64, rate float64) *capacityPlanner {
	p := &capacityPlanner{ctx: context.Background(), settings: capacityTestSettings, period: 60}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w", SLO: SLO{Percentile: 100, TargetLatency: time.Second}}, rate: rate}}
	for n, mu := range rates {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		item := capacityItem{id: fmt.Sprint("i", n), routingWeight: profileRoutingWeight(mu), throughput: mu, serviceNanos: 20e6, e: float64(n + 1), weight: 1}
		p.plans = append(p.plans, []*capacityPlan{{keep: true, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, record: SchedulerNodePlan{Key: "keep", Eligible: true}}})
		p.compared = append(p.compared, true)
	}
	return p
}

func TestCapacityDeltaClampBoundaries(t *testing.T) {
	beforePre, afterPost, beyondH := 0, 0, 0
	for _, horizon := range []float64{1, 10, 30} {
		p := e5Planner(map[int]float64{0: 20, 1: 40, 2: .3}, evaluationNodes(), e5Start)
		p.period = horizon
		tab := p.planTable()
		choice := make([]int, len(p.plans))
		for n := range choice {
			choice[n] = tab.byPlan[n][0]
		}
		s := newSearchState(p, tab, choice)
		for n, plans := range tab.byPlan {
			for _, q := range plans {
				key, _ := capacityDeltaAssert(t, p, s, n, q)
				if !tab.keep[q] {
					if min(key.route, horizon) < tab.tauPre[q] {
						beforePre++
					}
					if horizon-key.route < tab.tauPost[q] {
						afterPost++
					}
					if key.route > horizon {
						beyondH++
					}
				}
			}
		}
	}
	if beforePre == 0 || afterPost == 0 || beyondH == 0 {
		t.Fatalf("missing clamps: pre=%d post=%d beyond=%d", beforePre, afterPost, beyondH)
	}
}

func TestCapacityDeltaFrozenInputs(t *testing.T) {
	p := e5Planner(map[int]float64{0: 20}, evaluationScaled(2, 0), e5Start)
	tab := p.planTable()
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	s := newSearchState(p, tab, choice)
	table := p.scalarProblem(tab, time.Time{}).Table
	before := capacityDeltaSaved(capacityRank(s.delta(0, tab.byPlan[0][1]), table))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.ctx = ctx
	for _, plans := range p.plans {
		for _, plan := range plans {
			for _, instance := range plan.instances {
				instance.Profile.Batches = nil
			}
		}
	}
	p.snapshot.Instances, p.snapshot.Workloads, p.snapshot.Arrivals = nil, nil, nil
	after := capacityRank(s.delta(0, tab.byPlan[0][1]), table)
	if !reflect.DeepEqual(before, capacityDeltaSaved(after)) {
		t.Fatal("Delta read replay/raw inputs after freezing table")
	}
}

func TestCapacityDeltaIntegerRoutingIsNotCapacityGate(t *testing.T) {
	p := capacityDeltaTiny([]float64{1, 2}, 3)
	tab := p.planTable()
	s := newSearchState(p, tab, []int{tab.byPlan[0][0], tab.byPlan[1][0]})
	key, energy := capacityDeltaAssert(t, p, s, 0, s.choice[0])
	if key.weightsExceeded || !key.searchable || !key.feasible || energy.weights[0]["i0"] != 333 || energy.weights[0]["i1"] != 667 {
		t.Fatalf("integer route must not invent a capacity rejection: key=%+v weights=%v", key, energy.weights)
	}
}

func TestCapacityDeltaZeroWeightPrepare(t *testing.T) {
	p := capacityDeltaTiny([]float64{1e-9, 10}, 1)
	p.plans[0][0].items[0][0].prepare = true
	tab := p.planTable()
	s := newSearchState(p, tab, []int{tab.byPlan[0][0], tab.byPlan[1][0]})
	_, energy := capacityDeltaAssert(t, p, s, 0, s.choice[0])
	if !energy.unroutedPrepare {
		t.Fatal("finalist integer route must retain zero-weight prepare guard")
	}
}

func TestCapacityGreedyKeyOrder(t *testing.T) {
	p := capacityDeltaTiny([]float64{1, 2}, 0)
	for n := range p.plans {
		base := p.plans[n][0]
		for _, text := range []string{"", "a", "aa", "a|n1=", "z", "|"} {
			plan := *base
			plan.keep, plan.record.Key = false, text
			p.plans[n] = append(p.plans[n], &plan)
		}
	}
	tab := p.planTable()
	table := p.scalarProblem(tab, time.Time{}).Table
	var keys []protocol.Rank
	var texts []string
	for _, a := range tab.byPlan[0] {
		for _, b := range tab.byPlan[1] {
			s := newSearchState(p, tab, []int{a, b})
			key, energy := capacityDeltaAssert(t, p, s, 0, a)
			keys, texts = append(keys, capacityDeltaSaved(capacityRank(key, table))), append(texts, energy.key)
		}
	}
	for a := range keys {
		for b := range keys {
			if keys[a].CompareKey(keys[b]) != cmp.Compare(texts[a], texts[b]) {
				t.Fatalf("key order mismatch %q vs %q", texts[a], texts[b])
			}
		}
	}
}

var capacityDeltaBenchmarkResult protocol.Rank

func BenchmarkCapacityDelta(b *testing.B) {
	p := e5Planner(map[int]float64{0: 60, 1: 40, 2: 1.2}, evaluationScaled(2, 4), e5Start)
	tab := p.planTable()
	table := p.scalarProblem(tab, time.Time{}).Table
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	s := newSearchState(p, tab, choice)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		capacityDeltaBenchmarkResult = capacityRank(s.delta(0, tab.byPlan[0][1]), table)
	}
}

func TestCapacityDeltaNoAllocations(t *testing.T) {
	p := e5Planner(map[int]float64{0: 60, 1: 40, 2: 1.2}, evaluationScaled(2, 4), e5Start)
	tab := p.planTable()
	table := p.scalarProblem(tab, time.Time{}).Table
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	s := newSearchState(p, tab, choice)
	if got := testing.AllocsPerRun(100, func() { capacityDeltaBenchmarkResult = capacityRank(s.delta(0, tab.byPlan[0][1]), table) }); got != 0 {
		t.Fatalf("delta allocs/op=%g", got)
	}
	move := 0
	if got := testing.AllocsPerRun(100, func() { s.apply(0, tab.byPlan[0][move%2]); move++ }); got != 0 {
		t.Fatalf("apply allocs/op=%g", got)
	}
}

func TestCapacityDeltaBounds(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		maxReplicas, maxInstances int
		replicas                  bool
	}{
		{"total-including-others", 10, 4, true}, {"workload-replicas", 2, 100, true}, {"replicas-off", 100, 100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := capacityDeltaTiny([]float64{1, 2}, 1)
			p.ws[0].current, p.plans[0][0].others = 2, 2
			plan := *p.plans[0][0]
			plan.keep, plan.record.Key = false, "two"
			item := plan.items[0][0]
			item.id = "second"
			plan.items = [][]capacityItem{{plan.items[0][0], item}}
			p.plans[0] = append(p.plans[0], &plan)
			p.settings.MaxReplicas, p.settings.MaxInstances, p.settings.Controls.Replicas = tc.maxReplicas, tc.maxInstances, tc.replicas
			tab := p.planTable()
			s := newSearchState(p, tab, []int{tab.byPlan[0][0], tab.byPlan[1][0]})
			key, _ := capacityDeltaAssert(t, p, s, 0, tab.byPlan[0][1])
			if s.total != 4 || key.bounded {
				t.Fatalf("total=%d bounded=%t", s.total, key.bounded)
			}
			s.apply(0, tab.byPlan[0][1])
			if s.total != 5 || s.sums[0].count != 3 {
				t.Fatalf("total=%d count=%d", s.total, s.sums[0].count)
			}
		})
	}
}

func TestCapacityDeltaPendingReplicaBounds(t *testing.T) {
	p := capacityDeltaTiny([]float64{4}, 2)
	p.settings.MaxReplicas, p.settings.MaxInstances, p.ws[0].current = 2, 3, 2
	for _, pending := range []int{0, 1, 2} {
		plan := *p.plans[0][0]
		plan.pendingReplicas, plan.others = []int{pending}, pending
		p.plans[0] = append(p.plans[0], &plan)
	}
	for _, replicas := range []bool{true, false} {
		p.settings.Controls.Replicas = replicas
		tab := p.planTable()
		s := newSearchState(p, tab, []int{tab.byPlan[0][0]})
		for _, q := range tab.byPlan[0] {
			capacityDeltaAssert(t, p, s, 0, q)
			s.apply(0, q)
			capacityDeltaAssert(t, p, s, 0, q)
		}
	}
}
