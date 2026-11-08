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

func capacityDeltaAssert(t testing.TB, p *capacityPlanner, s *searchState, n, q int) (orderKey, capacityResult) {
	t.Helper()
	key := s.delta(n, q)
	choice := capacityDeltaChoice(s.table, key.choice)
	exact := p.evaluate(choice, p.routeOf(choice))
	if exact.bridge != nil {
		t.Fatal("bridge is outside this comparison fixture")
	}
	if key.feasible != exact.feasible || key.final != exact.final || !slices.Equal(key.excesses, exact.excesses) ||
		key.joules != exact.joules || key.keep != exact.keep || key.weightsExceeded != exact.weightsExceeded ||
		key.keepJoules != exact.keepJoules || key.period != exact.period ||
		key.unroutedPrepare != exact.unroutedPrepare || capacityDeltaKey(key) != exact.key || key.route != exact.route || key.bounded != p.bounded(choice) {
		t.Fatalf("choice=%v move=(%d,%d) delta={feasible:%t final:%t excess:%v J:%.17g keep:%t weightsExceeded:%t unroutedPrepare:%t route:%g key:%q bounded:%t} exact={feasible:%t final:%t excess:%v J:%.17g keep:%t weightsExceeded:%t unroutedPrepare:%t route:%g key:%q bounded:%t} exclusion=%q Jdiff=%.17g",
			choice, n, q, key.feasible, key.final, key.excesses, key.joules, key.keep, key.weightsExceeded, key.unroutedPrepare, key.route, capacityDeltaKey(key), key.bounded,
			exact.feasible, exact.final, exact.excesses, exact.joules, exact.keep, exact.weightsExceeded, exact.unroutedPrepare, exact.route, exact.key, p.bounded(choice), exact.exclusion, key.joules-exact.joules)
	}
	return key, exact
}

func TestCapacityDeltaExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261107, 3))
	samples, keyTies := 0, 0
	for _, space := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}} {
		for mix, rates := range e5Mixes {
			p := e5Planner(rates, evaluationScaled(space.variants, space.modes), e5Start, nil)
			for _, routing := range []bool{true, false} {
				if !routing && (space.variants != 1 || space.modes != 0 || mix >= 4) {
					continue
				}
				p.settings.Controls.Routing = routing
				count, ties := capacityDeltaWalk(t, p, rng)
				samples, keyTies = samples+count, keyTies+ties
			}
		}
	}
	if samples < 5000 {
		t.Fatalf("only %d comparison samples", samples)
	}
	t.Logf("samples=%d (routing ON=6912 OFF=384) key-ties=%d bridge-excluded=0 max-J-diff=0 aggregate-drift=0", samples, keyTies)
}

func TestCapacityDeltaClampBoundaries(t *testing.T) {
	samples, beforePre, afterPost, beyondH := 0, 0, 0, 0
	for _, horizon := range []float64{1, 10, 30} {
		options := evaluationNodes()
		for n := range options {
			for k := range options[n] {
				options[n][k].prepareS *= 4
				options[n][k].removeS *= 8
			}
		}
		p := e5Planner(map[int]float64{0: 20, 1: 40, 2: .3}, options, e5Start, nil)
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
				samples++
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
		t.Fatalf("missing clamp boundary: pre=%d post=%d beyond-H=%d", beforePre, afterPost, beyondH)
	}
	t.Logf("samples=%d min(r,H)<tauPre=%d H-r<tauPost=%d r>H=%d max-J-diff=0", samples, beforePre, afterPost, beyondH)
}

// Normal plans satisfy r>=preMax>=preMean. Explicit-route evaluation also
// accepts r<preMean; exercise that API boundary without inventing plan timings.
func TestCapacityDeltaExplicitRouteBeforePre(t *testing.T) {
	p := capacityDeltaTiny([]float64{4, 5}, 2)
	p.period = 3
	plan := *p.plans[0][0]
	plan.keep, plan.preMean, plan.preMax, plan.postMean = false, 5, 6, 7
	plan.preJ, plan.postJ, plan.mid, plan.after = 11, 13, 17, 19
	p.plans[0] = append(p.plans[0], &plan)
	tab := p.planTable()
	q, keep := tab.byPlan[0][1], tab.byPlan[1][0]
	for _, r := range []float64{0, 2} {
		s := newSearchState(p, tab, []int{q, keep})
		s.tPre[0], s.r = r, r
		s.nodeJ[0] = tab.energy(q, r, p.period)
		// A no-op on another node preserves the explicitly supplied route.
		key := s.delta(1, keep)
		exact := p.evaluate([]int{1, 0}, r)
		if key.route != r || key.joules != exact.joules {
			t.Fatalf("r=%g delta=%.17g exact=%.17g Jdiff=%.17g", r, key.joules, exact.joules, key.joules-exact.joules)
		}
	}
	t.Log("explicit r<tauPre samples=2 max-J-diff=0")
}

func TestCapacityDeltaPendingReplicaBounds(t *testing.T) {
	p := capacityDeltaTiny([]float64{4}, 2)
	p.settings.MaxReplicas, p.settings.MaxInstances = 2, 3
	p.ws[0].current = 2
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

func capacityDeltaWalk(t testing.TB, p *capacityPlanner, rng *rand.Rand) (int, int) {
	t.Helper()
	tab := p.planTable()
	table := p.scalarProblem(tab, time.Time{}).Table
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
	keyTies := 0
	for sample := range 96 {
		if sample%8 == 0 {
			for n := range choice {
				choice[n] = eligible[n][rng.IntN(len(eligible[n]))]
			}
			s.reset(choice)
		}
		n := rng.IntN(len(choice))
		q := eligible[n][rng.IntN(len(eligible[n]))]
		before := slices.Clone(s.choice)
		beforeSums, beforeTimes, beforeEnergy := slices.Clone(s.sums), slices.Clone(s.tPre), slices.Clone(s.nodeJ)
		beforeTotal, beforeRoute := s.total, s.r
		baseKey, baseExact := capacityDeltaAssert(t, p, s, n, s.choice[n])
		base := capacityDeltaSaved(capacityRank(baseKey, table))
		key, exact := capacityDeltaAssert(t, p, s, n, q)
		if !key.bounded {
			t.Fatal("harness generated an unbounded choice")
		}
		rank := capacityRank(key, table)
		if rank.Better(&base) != exact.better(&baseExact) || base.Better(&rank) != baseExact.better(&exact) {
			t.Fatalf("better mismatch sample=%d routing=%t", sample, p.settings.Controls.Routing)
		}
		if key.feasible == base.Feasible && key.final == base.Final && slices.Equal(key.excesses, base.Excesses) && key.joules == base.Joules && key.keep == base.Keep && exact.key != baseExact.key {
			keyTies++
		}
		if !slices.Equal(before, s.choice) || !slices.Equal(beforeSums, s.sums) || !slices.Equal(beforeTimes, s.tPre) ||
			!slices.Equal(beforeEnergy, s.nodeJ) || beforeTotal != s.total || beforeRoute != s.r {
			t.Fatal("delta changed search state")
		}
		s.apply(n, q)
		fresh.reset(key.choice)
		if !reflect.DeepEqual(s.sums, fresh.sums) || s.total != fresh.total || s.r != fresh.r || !slices.Equal(s.tPre, fresh.tPre) ||
			!slices.Equal(s.nodeJ, fresh.nodeJ) || !slices.Equal(s.workloads, fresh.workloads) {
			t.Fatalf("apply drift sample=%d sums=%+v fresh=%+v", sample, s.sums, fresh.sums)
		}
	}
	return 96, keyTies
}

func capacityDeltaTiny(kappas []float64, rate float64) *capacityPlanner {
	p := &capacityPlanner{ctx: context.Background(), settings: capacityTestSettings, period: 60}
	p.ws = []capacityWorkload{{d: &Definition{UID: "w"}, rate: rate, slack: 1}}
	for n, kappa := range kappas {
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{UID: fmt.Sprint("n", n)}})
		item := capacityItem{id: fmt.Sprint("i", n), kappa: kappa, e: float64(n + 1), weight: 1}
		p.plans = append(p.plans, []*capacityPlan{{keep: true, items: [][]capacityItem{{item}}, retained: [][]capacityItem{{item}}, record: SchedulerNodePlan{Key: "keep", Eligible: true}}})
		p.compared = append(p.compared, true)
	}
	return p
}

func TestCapacityDeltaRouteWeightExceedsCapacity(t *testing.T) {
	p := capacityDeltaTiny([]float64{1, 2}, 3)
	tab := p.planTable()
	s := newSearchState(p, tab, []int{tab.byPlan[0][0], tab.byPlan[1][0]})
	key, exact := capacityDeltaAssert(t, p, s, 0, s.choice[0])
	want := 3*float64(667)/1000/2 - 1
	if key.final || key.feasible || !key.weightsExceeded || key.excesses[0] != want || exact.weights[0]["i0"] != 333 || exact.weights[0]["i1"] != 667 {
		t.Fatalf("weights=%v final=%t exceeded=%t excess=%v want=%.17g", exact.weights, key.final, key.weightsExceeded, key.excesses, want)
	}
	t.Logf("weights=333:667 second-load=2.001 delta=exact excess=%.17g J=%.17g proposal-continuous-J=300", key.excesses[0], key.joules)
}

func TestCapacityDeltaZeroWeightPrepare(t *testing.T) {
	p := capacityDeltaTiny([]float64{1e-9, 10}, 1)
	p.plans[0][0].items[0][0].prepare = true
	tab := p.planTable()
	s := newSearchState(p, tab, []int{tab.byPlan[0][0], tab.byPlan[1][0]})
	key, _ := capacityDeltaAssert(t, p, s, 0, s.choice[0])
	if !key.unroutedPrepare {
		t.Fatal("zero-weight prepare must not be admitted")
	}
}

func TestCapacityGreedyKeyOrder(t *testing.T) {
	p := capacityDeltaTiny([]float64{1, 2}, 0)
	for n := range p.plans {
		base := p.plans[n][0]
		for _, text := range []string{"", "a", "aa", "a|n1=", "z", "|"} {
			plan := *base
			plan.keep = false
			plan.record.Key = text
			p.plans[n] = append(p.plans[n], &plan)
		}
	}
	tab := p.planTable()
	table := p.scalarProblem(tab, time.Time{}).Table
	var keys []protocol.Rank
	var exacts []capacityResult
	for _, a := range tab.byPlan[0] {
		for _, b := range tab.byPlan[1] {
			s := newSearchState(p, tab, []int{a, b})
			key, exact := capacityDeltaAssert(t, p, s, 0, a)
			keys, exacts = append(keys, capacityDeltaSaved(capacityRank(key, table))), append(exacts, exact)
		}
	}
	ties := 0
	for a, key := range keys {
		for b := range keys {
			if key.CompareKey(keys[b]) != cmp.Compare(exacts[a].key, exacts[b].key) || key.Better(&keys[b]) != exacts[a].better(&exacts[b]) {
				t.Fatalf("key order mismatch %q vs %q", exacts[a].key, exacts[b].key)
			}
			if exacts[a].key != exacts[b].key && key.Keep == keys[b].Keep {
				ties++
			}
		}
	}
	t.Logf("exact energy/excess/keep ties reaching logical key=%d", ties)
}

func TestCapacityGreedyWeightsAndJudge(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261107, 4))
	items, weights, load := make([]capacityItem, 8), make([]int32, 8), make([]float64, 8)
	for sample := range 10000 {
		n := rng.IntN(9)
		items := items[:n]
		for k := range items {
			items[k] = capacityItem{kappa: rng.Float64() * 100, weight: int32(rng.IntN(1001))}
			if sample%7 == 0 || rng.IntN(4) == 0 {
				items[k].kappa = 0
			}
		}
		rate := rng.Float64() * 500
		for _, routing := range []bool{true, false} {
			var wantWeights []int32
			var wantLoad []float64
			var wantOK bool
			if routing {
				wantWeights, wantLoad, wantOK = capacityWeights(rate, items)
			} else {
				wantWeights, wantLoad, wantOK = capacityFixedWeights(rate, items)
			}
			ok := capacitySearchWeights(rate, items, weights[:n], load[:n], routing)
			if ok != wantOK || !slices.Equal(weights[:n], wantWeights) || !slices.Equal(load[:n], wantLoad) {
				t.Fatalf("weights sample=%d routing=%t got=%v/%v/%t want=%v/%v/%t", sample, routing, weights[:n], load[:n], ok, wantWeights, wantLoad, wantOK)
			}
		}
		w := capacityWorkload{rate: rate, queued: rng.IntN(5), slack: float64(rng.IntN(5) - 1)}
		knew, kret, r := rng.Float64()*500, rng.Float64()*500, rng.Float64()*20
		if sample%3 == 0 {
			knew = 0
		}
		if sample%5 == 0 {
			w.rate = 0
		}
		present := sample%2 == 0
		j := capacityJudge(w, knew, kret, present, r)
		satisfied, final, worst := capacitySearchJudge(w, knew, kret, present, r)
		if satisfied != j.satisfied || final != (j.final != nil && *j.final <= 0) || worst != j.worst() {
			t.Fatalf("judge sample=%d got=%t/%t/%g exact=%+v worst=%g", sample, satisfied, final, worst, j, j.worst())
		}
	}
}

var capacityDeltaBenchmarkResult protocol.Rank

func BenchmarkCapacityDelta(b *testing.B) {
	p := e5Planner(map[int]float64{0: 60, 1: 40, 2: 1.2}, evaluationScaled(2, 4), e5Start, nil)
	tab := p.planTable()
	table := p.scalarProblem(tab, time.Time{}).Table
	rng := rand.New(rand.NewPCG(20261107, 5))
	choice := make([]int, len(p.plans))
	for n, plans := range tab.byPlan {
		choice[n] = plans[rng.IntN(len(plans))]
	}
	s := newSearchState(p, tab, choice)
	moves := [][2]int{}
	for n, plans := range tab.byPlan {
		for _, q := range plans {
			if tab.searchable[q] {
				moves = append(moves, [2]int{n, q})
			}
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		move := moves[i%len(moves)]
		capacityDeltaBenchmarkResult = capacityRank(s.delta(move[0], move[1]), table)
	}
}

func TestCapacityDeltaNoAllocations(t *testing.T) {
	p := e5Planner(map[int]float64{0: 60, 1: 40, 2: 1.2}, evaluationScaled(2, 4), e5Start, nil)
	tab := p.planTable()
	table := p.scalarProblem(tab, time.Time{}).Table
	choice := make([]int, len(p.plans))
	for n := range choice {
		choice[n] = tab.byPlan[n][0]
	}
	s := newSearchState(p, tab, choice)
	if got := testing.AllocsPerRun(100, func() { capacityDeltaBenchmarkResult = capacityRank(s.delta(0, tab.byPlan[0][1]), table) }); got != 0 {
		t.Fatalf("delta allocs/op=%g want=0", got)
	}
	move := 0
	if got := testing.AllocsPerRun(100, func() {
		s.apply(0, tab.byPlan[0][move%2])
		move++
	}); got != 0 {
		t.Fatalf("apply allocs/op=%g want=0", got)
	}
	if math.IsNaN(capacityDeltaBenchmarkResult.Joules) {
		t.Fatal("invalid benchmark result")
	}
}

func TestCapacityDeltaBounds(t *testing.T) {
	p := capacityDeltaTiny([]float64{1, 2}, 1)
	p.ws[0].current = 2
	p.plans[0][0].others = 2
	plan := *p.plans[0][0]
	plan.keep, plan.record.Key = false, "two"
	item := plan.items[0][0]
	item.id = "second"
	plan.items = [][]capacityItem{{plan.items[0][0], item}}
	p.plans[0] = append(p.plans[0], &plan)
	for _, tc := range []struct {
		name                      string
		maxReplicas, maxInstances int
		replicas                  bool
	}{
		{"total-including-others", 10, 4, true},
		{"workload-replicas", 2, 100, true},
		{"replicas-off", 100, 100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.settings.MaxReplicas, p.settings.MaxInstances, p.settings.Controls.Replicas = tc.maxReplicas, tc.maxInstances, tc.replicas
			tab := p.planTable()
			s := newSearchState(p, tab, []int{tab.byPlan[0][0], tab.byPlan[1][0]})
			key, _ := capacityDeltaAssert(t, p, s, 0, tab.byPlan[0][1])
			if s.total != 4 || key.bounded {
				t.Fatalf("before total=%d moved bounded=%t", s.total, key.bounded)
			}
			s.apply(0, tab.byPlan[0][1])
			if s.total != 5 || s.sums[0].count != 3 {
				t.Fatalf("moved total=%d count=%d", s.total, s.sums[0].count)
			}
		})
	}
}
