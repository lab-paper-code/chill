package serving

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// e5Option is one measured mobilenet option of m4 r7 (2026-10-21): κ from
// the recorded node plans (dispatch-calibrated, tight SLO, b1), e (J/input, b1)
// and loaded idle watts from the snapshot profiles, prepare seconds and
// whole-wall joules from the recorded plan chains.
type e5Option struct {
	name                 string
	kappa, e, idle       float64
	prepareS, prepareJ   float64
	plc, vlmCPU, vlmCUDA bool // derives the synthetic PLC / SmolVLM-like options
	unloaded, removeS    float64
	removeJ              float64
	node                 string
}

// e5Nodes are the five nodes m4 r7 planned on (Jetsons excluded), with the
// unloaded node baseline and the measured remove chain of each node.
func e5Nodes() [][]e5Option {
	node := func(name string, unloaded, removeS, removeJ float64, options ...e5Option) []e5Option {
		for k := range options {
			options[k].node, options[k].unloaded, options[k].removeS, options[k].removeJ = name, unloaded, removeS, removeJ
		}
		return options
	}
	return [][]e5Option{
		node("rasp5", 4.8866, 3.603, 18.092,
			e5Option{name: "q1", kappa: 25.288, e: 0.07068, idle: 4.8985, prepareS: 19.256, prepareJ: 113.906, plc: true, vlmCPU: true},
			e5Option{name: "q1-lut", kappa: 25.688, e: 0.07453, idle: 4.9041, prepareS: 20.190, prepareJ: 120.089},
			e5Option{name: "q2", kappa: 29.300, e: 0.09886, idle: 4.8768, prepareS: 15.926, prepareJ: 101.666}),
		node("orangepi", 2.7264, 3.979, 11.378,
			e5Option{name: "q1", kappa: 7.533, e: 0.09943, idle: 2.7128, prepareS: 36.859, prepareJ: 126.737, plc: true}),
		node("lattepanda", 9.3047, 3.695, 34.689,
			e5Option{name: "q1", kappa: 30.067, e: 0.12079, idle: 9.4955, prepareS: 18.278, prepareJ: 202.782, plc: true, vlmCPU: true},
			e5Option{name: "q2", kappa: 39.079, e: 0.13780, idle: 9.4869, prepareS: 15.764, prepareJ: 177.609}),
		node("genesis2", 54.0631, 4.361, 232.881,
			e5Option{name: "cpu-q1", kappa: 82.901, e: 0.32292, idle: 54.8725, prepareS: 12.357, prepareJ: 756.903, plc: true, vlmCPU: true},
			e5Option{name: "cuda-q1", kappa: 104.338, e: 0.26299, idle: 109.6394, prepareS: 13.159, prepareJ: 1164.312, vlmCUDA: true}),
		node("genesis3", 49.9728, 4.236, 211.444,
			e5Option{name: "cpu-q1", kappa: 63.596, e: 0.27643, idle: 51.6730, prepareS: 13.110, prepareJ: 712.615, plc: true, vlmCPU: true},
			e5Option{name: "cpu-q2", kappa: 69.151, e: 0.32284, idle: 49.7983, prepareS: 10.681, prepareJ: 635.085}),
	}
}

// e5Scaled grows e5Nodes toward the E5 space: every option gets a b2 variant
// (κ 1.3×, e 0.9×) when variants is 2, and copies of genesis3, rasp5 and
// lattepanda stand in for jetsono, jetsonx and jetson-nano with modes power
// modes each (mode m: κ and loaded watts (1 − 0.15m)×, prepare (1 + 0.1m)×).
func e5Scaled(variants, modes int) [][]e5Option {
	nodes := e5Nodes()
	if modes > 0 {
		for i, name := range []string{"jetsono", "jetsonx", "jetson-nano"} {
			k := []int{4, 0, 2}[i]
			copied := []e5Option{}
			for m := range modes {
				for _, o := range nodes[k] {
					f := 1 - 0.15*float64(m)
					o.node, o.name = name, fmt.Sprint(o.name, "@", m)
					o.kappa, o.idle, o.prepareS = f*o.kappa, o.unloaded+f*(o.idle-o.unloaded), (1+0.1*float64(m))*o.prepareS
					copied = append(copied, o)
				}
			}
			nodes = append(nodes, copied)
		}
	}
	for n := range nodes {
		for _, o := range slices.Clone(nodes[n]) {
			if variants > 1 {
				o.name, o.kappa, o.e = o.name+"-b2", 1.3*o.kappa, 0.9*o.e
				nodes[n] = append(nodes[n], o)
			}
		}
	}
	return nodes
}

// e5Workloads: mobilenet (measured), PLC-like (synthetic: κ 1.5×, e 0.4×
// of the node's q1 CPU option) and SmolVLM-like (synthetic: CPU int8 κ/120,
// e×240; CUDA κ/50, e×150; prepare 2×). L − m: 1 s − 29 ms; 10 s − 29 ms.
var e5Workloads = []struct {
	name  string
	slack float64
}{{"mnv2", 0.971}, {"plc", 0.971}, {"vlm", 9.971}}

// e5Pair is one instance a node plan can hold: Workload w on an option.
type e5Pair struct {
	w   int
	id  string
	opt e5Option
}

func e5Pairs(options []e5Option) []e5Pair {
	pairs := []e5Pair{}
	add := func(w int, o e5Option, suffix string) {
		pairs = append(pairs, e5Pair{w: w, id: o.node + "/" + e5Workloads[w].name + "/" + o.name + suffix, opt: o})
	}
	for _, o := range options {
		add(0, o, "")
		if o.plc {
			p := o
			p.kappa, p.e = 1.5*o.kappa, 0.4*o.e
			add(1, p, "")
		}
		if o.vlmCPU || o.vlmCUDA {
			v := o
			v.kappa, v.e, v.prepareS, v.prepareJ = o.kappa/120, 240*o.e, 2*o.prepareS, 2*o.prepareJ
			if o.vlmCUDA {
				v.kappa, v.e = o.kappa/50, 150*o.e
			}
			add(2, v, "-vlm")
		}
	}
	return pairs
}

// e5Start is the first configuration, one model per node as the planner
// places them: mobilenet on rasp5 q1, orangepi q1 and genesis3 q2 (κ 102.0),
// PLC on lattepanda (κ 45.1), SmolVLM on genesis2 CUDA (κ 2.09).
var e5Start = map[string]bool{
	"rasp5/mnv2/q1": true, "orangepi/mnv2/q1": true, "genesis3/mnv2/cpu-q2": true,
	"lattepanda/plc/q1": true, "genesis2/vlm/cuda-q1-vlm": true,
}

// e5IdleHalfWidth is the median model-loaded idle half-width relative to the
// idle watts over the 11 BEO results held locally (rasp5 5, lattepanda 4,
// genesis2 2; 0.95, three trials each): 0.84–9.95%, median 1.84%. They record
// no per-input interval, so the fixture's request energy carries none.
const e5IdleHalfWidth = 0.0184

// e5Planner builds the node plans the planner would hold for the pairs from
// the current instances: keep, each pair alone (one model and one instance per
// Workload on a Node) and removing every instance. plans[n][0] keeps the
// current instances at the routed weights (κ-proportional when routed is nil).
// Removals run after the route.
func e5Planner(rates map[int]float64, nodes [][]e5Option, current map[string]bool, routed map[string]int32) *capacityPlanner {
	s := capacityTestSettings
	s.ComparisonPeriod, s.MaxReplicas, s.MaxInstances, s.PlanSearchWorkers = 10*time.Minute, 8, 16, 3
	p := &capacityPlanner{ctx: context.Background(), settings: s, index: map[string]int{}}
	p.keep.Routing = map[string]map[string]int32{}
	index := map[int]int{}
	for w := range e5Workloads {
		if rate, ok := rates[w]; ok {
			index[w] = len(p.ws)
			uid := e5Workloads[w].name
			p.index[uid] = len(p.ws)
			p.ws = append(p.ws, capacityWorkload{d: &Definition{UID: uid}, rate: rate, minRate: rate, slack: e5Workloads[w].slack})
			p.keep.Routing[uid] = map[string]int32{}
		}
	}
	weights := map[string]int32{}
	for uid := range p.index {
		currents := []e5Pair{}
		total := 0.0
		for _, options := range nodes {
			for _, c := range e5Pairs(options) {
				if current[c.id] && e5Workloads[c.w].name == uid {
					currents, total = append(currents, c), total+c.opt.kappa
				}
			}
		}
		left := int32(schedulerRouteUnits)
		for k, c := range currents {
			weights[c.id] = int32(math.Floor(c.opt.kappa / total * schedulerRouteUnits))
			if k == len(currents)-1 {
				weights[c.id] = left
			}
			left -= weights[c.id]
			if routed != nil {
				weights[c.id] = routed[c.id]
			}
			if weights[c.id] > 0 {
				p.keep.Routing[uid][c.id] = weights[c.id]
			}
		}
	}
	pre, post := 0.0, 0.0
	for _, options := range nodes {
		pairs := slices.DeleteFunc(e5Pairs(options), func(c e5Pair) bool { _, ok := index[c.w]; return !ok })
		now := slices.DeleteFunc(slices.Clone(pairs), func(c e5Pair) bool { return !current[c.id] })
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{Name: options[0].node, UID: options[0].node}})
		watts := func(set []e5Pair) float64 {
			x := options[0].unloaded
			for _, c := range set {
				x += c.opt.idle - c.opt.unloaded
			}
			return x
		}
		plan := func(set []e5Pair, keep bool) *capacityPlan {
			k := len(p.ws)
			plan := &capacityPlan{keep: keep, items: make([][]capacityItem, k), retained: make([][]capacityItem, k), before: watts(now)}
			ids := []string{}
			mid := slices.Clone(now)
			for _, c := range set {
				w := index[c.w]
				item := capacityItem{id: c.id, kappa: c.opt.kappa, e: c.opt.e, weight: weights[c.id], row: c.id, prepare: !current[c.id]}
				if !current[c.id] {
					plan.preMean, plan.preMax, plan.preJ = plan.preMean+c.opt.prepareS, plan.preMax+c.opt.prepareS, plan.preJ+c.opt.prepareJ
					mid = append(mid, c)
				}
				plan.items[w] = append(plan.items[w], item)
				plan.instances = append(plan.instances, e5Instance(c))
				ids = append(ids, c.id)
			}
			for _, c := range now {
				plan.retained[index[c.w]] = append(plan.retained[index[c.w]], capacityItem{id: c.id, kappa: c.opt.kappa, e: c.opt.e, weight: weights[c.id], row: c.id})
				if !slices.ContainsFunc(set, func(x e5Pair) bool { return x.id == c.id }) {
					plan.postMean, plan.postMax, plan.postJ = plan.postMean+c.opt.removeS, plan.postMax+c.opt.removeS, plan.postJ+c.opt.removeJ
				}
			}
			for w := range plan.items {
				slices.SortFunc(plan.items[w], func(a, b capacityItem) int { return cmp.Or(cmp.Compare(a.e, b.e), cmp.Compare(a.id, b.id)) })
			}
			plan.mid, plan.after = watts(mid), watts(set)
			for _, c := range mid {
				plan.midInstances = append(plan.midInstances, e5Instance(c))
			}
			plan.replaces = len(mid) > len(now) && plan.postMean > 0
			plan.record.Eligible, plan.record.Key = true, strings.Join(ids, "+")
			p.sum(plan)
			if !keep {
				pre, post = max(pre, plan.preMax), max(post, plan.postMax)
			}
			return plan
		}
		plans := []*capacityPlan{plan(now, true)}
		for _, c := range pairs {
			if len(now) != 1 || now[0].id != c.id {
				plans = append(plans, plan([]e5Pair{c}, false))
			}
		}
		if len(now) > 0 {
			plans = append(plans, plan(nil, false)) // remove every instance
		}
		p.plans = append(p.plans, plans)
		p.compared = append(p.compared, true)
	}
	p.period = max(s.ComparisonPeriod.Seconds(), pre+post)
	return p
}

// e5Brute is capacityBruteForce split over the plans of the first Node,
// eight at a time.
func e5Brute(p *capacityPlanner) *capacityResult {
	results := make([]*capacityResult, len(p.plans[0]))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for j := range p.plans[0] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			q := *p
			q.plans = slices.Clone(p.plans)
			q.plans[0] = p.plans[0][j : j+1]
			if res := capacityBruteForce(&q); res != nil {
				res.choice[0] = j
				results[j] = res
			}
		}()
	}
	wg.Wait()
	var best *capacityResult
	for _, res := range results {
		if res != nil && res.better(best) {
			best = res
		}
	}
	return best
}

// e5Instance is the instance of a pair; its profile, one per option, carries
// the idle half-width.
func e5Instance(c e5Pair) SchedulerInstance {
	return SchedulerInstance{ID: c.id, Profile: &Profile{ID: c.id, IdleBaselineHalfWidthWatts: e5IdleHalfWidth * c.opt.idle}}
}

// e5Converge repeats Ticks, each from the configuration and route weights the
// previous decision chose (after payback and minimum gain), until a Tick keeps
// both or returns to a configuration and weights it left (a cycle), at most
// 10 Ticks. It returns the first Tick's search result, the energy over T of
// keeping the last configuration, the Ticks taken (−1 without a fixed point or
// cycle) and whether it cycled. A nil result keeps.
func e5Converge(rates map[int]float64, nodes [][]e5Option, solve func(*capacityPlanner) *capacityResult) (*capacityResult, float64, int, bool) {
	current := e5Start
	var routed map[string]int32
	var first *capacityResult
	seen := map[string]bool{}
	for tick := 1; tick <= 10; tick++ {
		p := e5Planner(rates, nodes, current, routed)
		keepJ := p.evaluate(make([]int, len(p.plans)), 0).joules
		key := fmt.Sprint(current, routed) // fmt sorts map keys
		if seen[key] {
			return first, keepJ, tick, true
		}
		seen[key] = true
		res := solve(p)
		if tick == 1 {
			first = res
		}
		if res != nil {
			paid := p.minimumGain(p.payback(*res)) // as decide receives it
			res = &paid
		}
		next, now := map[string]int32{}, map[string]int32{}
		for _, weights := range p.keep.Routing {
			maps.Copy(now, weights)
		}
		if res != nil {
			for _, weights := range res.weights {
				maps.Copy(next, weights)
			}
		}
		if res == nil || res.keep && maps.Equal(next, now) {
			return first, keepJ, tick, false
		}
		current, routed = map[string]bool{}, next
		for n, j := range res.choice {
			for _, items := range p.plans[n][j].items {
				for _, item := range items {
					current[item.id] = true
				}
			}
		}
	}
	return first, math.NaN(), -1, false
}

var e5Mixes = func() []map[int]float64 {
	mixes := []map[int]float64{}
	for _, mnv2 := range []float64{20, 60, 110} {
		mixes = append(mixes, map[int]float64{0: mnv2, 1: 15}, map[int]float64{0: mnv2, 1: 40}, map[int]float64{0: mnv2, 2: 0.3}, map[int]float64{0: mnv2, 2: 1.2},
			map[int]float64{0: mnv2, 1: 15, 2: 0.3}, map[int]float64{0: mnv2, 1: 15, 2: 1.2}, map[int]float64{0: mnv2, 1: 40, 2: 0.3}, map[int]float64{0: mnv2, 1: 40, 2: 1.2})
	}
	return mixes
}()

// Plan search at E5 scale (capacity-table-evidence §7). Gap: the planner's
// search (keep and emptied starts, k = 2, then 3; no budget) against the
// exact minimum on 24 load mixes, for one decision and over repeated Ticks
// (each from the previous choice after payback and minimum gain) until a
// fixed point or a cycle (energy of keeping the last configuration), over the
// r7 nodes, the same nodes with b2 variants and 8 Nodes (b1, 2 power modes on
// the Jetson copies). Scale: search time with 3 workers and the 5 s budget of
// a 10 s control interval as e5Scaled grows the space toward E5. About five minutes:
// CHILL_PLAN_SEARCH=1 go test ./internal/serving -run PlanSearchE5 -v -timeout 0.
func TestCapacityPlanSearchE5(t *testing.T) {
	if os.Getenv("CHILL_PLAN_SEARCH") == "" {
		t.Skip("measurement sweep; set CHILL_PLAN_SEARCH=1")
	}
	search := func(p *capacityPlanner) *capacityResult { r := capacitySearchForTest(t, p); return &r }
	for _, c := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}} {
		nodes := e5Scaled(c.variants, c.modes)
		differs, worst, worstFixed, bestFixed, over, cycles, exactCycles, maxTicks, maxExactTicks := 0, 0.0, 0.0, 0.0, 0, 0, 0, 0, 0
		for _, rates := range e5Mixes {
			got, gotJ, ticks, cycled := e5Converge(rates, nodes, search)
			exact, exactJ, exactTicks, exactCycled := e5Converge(rates, nodes, e5Brute)
			if exact == nil {
				if got.feasible {
					t.Errorf("%+v %v: search feasible, exact infeasible", c, rates)
				}
				continue
			}
			if !got.feasible {
				t.Errorf("%+v %v: search infeasible, exact feasible", c, rates)
				continue
			}
			gap, fixed := (got.joules-exact.joules)/exact.joules, (gotJ-exactJ)/exactJ
			maxTicks, maxExactTicks = max(maxTicks, ticks), max(maxExactTicks, exactTicks)
			if got.key != exact.key {
				differs++
			}
			if cycled {
				cycles++
			}
			if exactCycled {
				exactCycles++
			}
			if fixed > 0.005 {
				over++
			}
			worst, worstFixed, bestFixed = max(worst, gap), max(worstFixed, fixed), min(bestFixed, fixed)
			if gap > 0.005 || fixed > 0.005 || fixed < -0.005 || ticks < 0 || cycled || exactCycled {
				t.Logf("ROW %+v %v one=%.3f%% fixed=%.3f%% ticks=%d cycled=%v exactTicks=%d exactCycled=%v\n  search: %s\n  exact:  %s",
					c, rates, 100*gap, 100*fixed, ticks, cycled, exactTicks, exactCycled, got.key, exact.key)
			}
		}
		t.Logf("GAP variants=%d modes=%d mixes=%d differs=%d worstOne=%.3f%% fixed: worst=%.3f%% best=%.3f%% over0.5%%=%d cycles=%d exactCycles=%d ticks<=%d exactTicks<=%d",
			c.variants, c.modes, len(e5Mixes), differs, 100*worst, 100*worstFixed, 100*bestFixed, over, cycles, exactCycles, maxTicks, maxExactTicks)
	}
	rates := map[int]float64{0: 60, 1: 40, 2: 1.2}
	for _, c := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}, {1, 4}, {2, 1}, {2, 2}, {2, 4}} {
		p := e5Planner(rates, e5Scaled(c.variants, c.modes), e5Start, nil)
		counts, sets := []int{}, 1.0
		for _, plans := range p.plans {
			counts, sets = append(counts, len(plans)), sets*float64(len(plans))
		}
		budgeted, err := p.search(time.Now().Add(5 * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		cut := p.searched
		p.searched = SchedulerPlanSearch{}
		full := search(p)
		t.Logf("SCALE variants=%d modes=%d plans=%v sets=%.3g\n  budget 5 s: k=%d evaluated=%d %v cut=%v %.1f J\n  no budget:  k=%d evaluated=%d %v %.1f J",
			c.variants, c.modes, counts, sets, cut.Neighborhood, cut.Evaluated, cut.Duration.Round(time.Millisecond), cut.BudgetCut, budgeted.joules,
			p.searched.Neighborhood, p.searched.Evaluated, p.searched.Duration.Round(time.Millisecond), full.joules)
	}
}
