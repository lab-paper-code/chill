package serving

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// cellsOption is one measured mobilenet option of m4 r7 (2026-10-21): κ from
// the recorded node plans (dispatch-calibrated, tight SLO, b1), e (J/input, b1)
// and loaded idle watts from the snapshot profiles, prepare seconds and
// whole-wall joules from the recorded plan chains.
type cellsOption struct {
	name                 string
	kappa, e, idle       float64
	prepareS, prepareJ   float64
	cuda                 bool
	plc, vlmCPU, vlmCUDA bool // derives the synthetic PLC / SmolVLM-like options
	unloaded, removeS    float64
	removeJ              float64
	node                 string
	limit                int
	group                string // the option a batch variant belongs to: one variant per plan
	mode                 int    // power mode: one mode per plan
}

// cellsNodes are the five nodes m4 r7 planned on (Jetsons excluded), with the
// unloaded node baseline and the measured remove chain of each node.
func cellsNodes() [][]cellsOption {
	node := func(name string, unloaded, removeS, removeJ float64, limit int, options ...cellsOption) []cellsOption {
		for k := range options {
			options[k].node, options[k].unloaded, options[k].removeS, options[k].removeJ, options[k].limit = name, unloaded, removeS, removeJ, limit
			options[k].group = options[k].name
		}
		return options
	}
	return [][]cellsOption{
		node("rasp5", 4.8866, 3.603, 18.092, 2,
			cellsOption{name: "q1", kappa: 25.288, e: 0.07068, idle: 4.8985, prepareS: 19.256, prepareJ: 113.906, plc: true, vlmCPU: true},
			cellsOption{name: "q1-lut", kappa: 25.688, e: 0.07453, idle: 4.9041, prepareS: 20.190, prepareJ: 120.089},
			cellsOption{name: "q2", kappa: 29.300, e: 0.09886, idle: 4.8768, prepareS: 15.926, prepareJ: 101.666}),
		node("orangepi", 2.7264, 3.979, 11.378, 2,
			cellsOption{name: "q1", kappa: 7.533, e: 0.09943, idle: 2.7128, prepareS: 36.859, prepareJ: 126.737, plc: true}),
		node("lattepanda", 9.3047, 3.695, 34.689, 2,
			cellsOption{name: "q1", kappa: 30.067, e: 0.12079, idle: 9.4955, prepareS: 18.278, prepareJ: 202.782, plc: true, vlmCPU: true},
			cellsOption{name: "q2", kappa: 39.079, e: 0.13780, idle: 9.4869, prepareS: 15.764, prepareJ: 177.609}),
		node("genesis2", 54.0631, 4.361, 232.881, 3,
			cellsOption{name: "cpu-q1", kappa: 82.901, e: 0.32292, idle: 54.8725, prepareS: 12.357, prepareJ: 756.903, plc: true, vlmCPU: true},
			cellsOption{name: "cuda-q1", kappa: 104.338, e: 0.26299, idle: 109.6394, prepareS: 13.159, prepareJ: 1164.312, cuda: true, vlmCUDA: true}),
		node("genesis3", 49.9728, 4.236, 211.444, 3,
			cellsOption{name: "cpu-q1", kappa: 63.596, e: 0.27643, idle: 51.6730, prepareS: 13.110, prepareJ: 712.615, plc: true, vlmCPU: true},
			cellsOption{name: "cpu-q2", kappa: 69.151, e: 0.32284, idle: 49.7983, prepareS: 10.681, prepareJ: 635.085}),
	}
}

// cellsScaled grows cellsNodes toward the E5 space: every option gets a b2
// variant (κ 1.3×, e 0.9×) when variants is 2, and copies of genesis3, rasp5
// and lattepanda stand in for jetsono, jetsonx and jetson-nano with modes power
// modes each (mode m: κ and loaded watts (1 − 0.15m)×, prepare (1 + 0.1m)×).
func cellsScaled(variants, modes int) [][]cellsOption {
	nodes := cellsNodes()
	if modes > 0 {
		for i, name := range []string{"jetsono", "jetsonx", "jetson-nano"} {
			k := []int{4, 0, 2}[i]
			copied := []cellsOption{}
			for m := range modes {
				for _, o := range nodes[k] {
					f := 1 - 0.15*float64(m)
					o.node, o.mode, o.name = name, m, fmt.Sprint(o.name, "@", m)
					o.group, o.kappa, o.idle, o.prepareS = o.name, f*o.kappa, o.unloaded+f*(o.idle-o.unloaded), (1+0.1*float64(m))*o.prepareS
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

// cellsWorkloads: mobilenet (measured), PLC-like (synthetic: κ 1.5×, e 0.4×
// of the node's q1 CPU option) and SmolVLM-like (synthetic: CPU int8 κ/120,
// e×240; CUDA κ/50, e×150; prepare 2×). L − m: 1 s − 29 ms; 10 s − 29 ms.
var cellsWorkloads = []struct {
	name  string
	slack float64
}{{"mnv2", 0.971}, {"plc", 0.971}, {"vlm", 9.971}}

// cellsPair is one instance a node plan can hold: Workload w on an option.
type cellsPair struct {
	w   int
	id  string
	opt cellsOption
}

func cellsPairs(options []cellsOption) []cellsPair {
	pairs := []cellsPair{}
	add := func(w int, o cellsOption, suffix string) {
		pairs = append(pairs, cellsPair{w: w, id: o.node + "/" + cellsWorkloads[w].name + "/" + o.name + suffix, opt: o})
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

// cellsCurrent is the starting configuration, one model per node as the
// planner places them: mobilenet on rasp5 q1, orangepi q1 and genesis3 q2
// (κ 102.0), PLC on lattepanda (κ 45.1), SmolVLM on genesis2 CUDA (κ 2.09).
var cellsCurrent = map[string]bool{
	"rasp5/mnv2/q1": true, "orangepi/mnv2/q1": true, "genesis3/mnv2/cpu-q2": true,
	"lattepanda/plc/q1": true, "genesis2/vlm/cuda-q1-vlm": true,
}

// cellsPlanner builds the node plans the planner would hold for the pairs: per
// node every set of at most limit instances of one model and one power mode,
// one variant per option and at most one CUDA instance (nodePlans holds one
// model per node plan); plans[n][0] keeps
// the current instances. Removals run after the route.
func cellsPlanner(rates map[int]float64, nodes [][]cellsOption) *capacityPlanner {
	s := capacityTestSettings
	s.ComparisonPeriod, s.MaxReplicas, s.MaxInstances = 10*time.Minute, 8, 16
	p := &capacityPlanner{ctx: context.Background(), settings: s, index: map[string]int{}}
	p.keep.Routing = map[string]map[string]int32{}
	index := map[int]int{}
	for w := range cellsWorkloads {
		if rate, ok := rates[w]; ok {
			index[w] = len(p.ws)
			uid := cellsWorkloads[w].name
			p.index[uid] = len(p.ws)
			p.ws = append(p.ws, capacityWorkload{d: &Definition{UID: uid}, rate: rate, minRate: rate, slack: cellsWorkloads[w].slack})
			p.keep.Routing[uid] = map[string]int32{}
		}
	}
	// Current route weights follow κ and sum to schedulerRouteUnits.
	weights := map[string]int32{}
	for uid := range p.index {
		currents := []cellsPair{}
		total := 0.0
		for _, options := range nodes {
			for _, c := range cellsPairs(options) {
				if cellsCurrent[c.id] && cellsWorkloads[c.w].name == uid {
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
			p.keep.Routing[uid][c.id] = weights[c.id]
		}
	}
	pre, post := 0.0, 0.0
	routes := []float64{0}
	for _, options := range nodes {
		pairs := slices.DeleteFunc(cellsPairs(options), func(c cellsPair) bool { _, ok := index[c.w]; return !ok })
		current := slices.DeleteFunc(slices.Clone(pairs), func(c cellsPair) bool { return !cellsCurrent[c.id] })
		p.snapshot.Nodes = append(p.snapshot.Nodes, SchedulerNode{Node: NodeReference{Name: options[0].node, UID: options[0].node}})
		watts := func(set []cellsPair) float64 {
			x := options[0].unloaded
			for _, c := range set {
				x += c.opt.idle - c.opt.unloaded
			}
			return x
		}
		plan := func(set []cellsPair, keep bool) *capacityPlan {
			k := len(p.ws)
			plan := &capacityPlan{keep: keep, items: make([][]capacityItem, k), retained: make([][]capacityItem, k), before: watts(current)}
			ids := []string{}
			mid := slices.Clone(current)
			for _, c := range set {
				w := index[c.w]
				item := capacityItem{id: c.id, kappa: c.opt.kappa, e: c.opt.e, weight: weights[c.id], row: c.id}
				if !cellsCurrent[c.id] {
					plan.preMean, plan.preMax, plan.preJ = plan.preMean+c.opt.prepareS, plan.preMax+c.opt.prepareS, plan.preJ+c.opt.prepareJ
					mid = append(mid, c)
				}
				plan.items[w] = append(plan.items[w], item)
				ids = append(ids, c.id)
			}
			for _, c := range current {
				plan.retained[index[c.w]] = append(plan.retained[index[c.w]], capacityItem{id: c.id, kappa: c.opt.kappa, e: c.opt.e, weight: weights[c.id], row: c.id})
				if !slices.ContainsFunc(set, func(x cellsPair) bool { return x.id == c.id }) {
					plan.postMean, plan.postMax, plan.postJ = plan.postMean+c.opt.removeS, plan.postMax+c.opt.removeS, plan.postJ+c.opt.removeJ
				}
			}
			for w := range plan.items {
				slices.SortFunc(plan.items[w], func(a, b capacityItem) int { return cmp.Or(cmp.Compare(a.e, b.e), cmp.Compare(a.id, b.id)) })
			}
			plan.mid, plan.after = watts(mid), watts(set)
			plan.record.Eligible, plan.record.Key = true, strings.Join(ids, "+")
			p.sum(plan)
			if !keep {
				pre, post = max(pre, plan.preMax), max(post, plan.postMax)
				routes = append(routes, plan.preMax)
			}
			return plan
		}
		plans := []*capacityPlan{plan(current, true)}
		for _, set := range cellsSets(pairs, current, options[0].limit) {
			plans = append(plans, plan(set, false))
		}
		if len(current) > 0 {
			plans = append(plans, plan(nil, false)) // remove every instance
		}
		p.plans = append(p.plans, plans)
		p.compared = append(p.compared, true)
	}
	p.elideCounts()
	p.period = max(s.ComparisonPeriod.Seconds(), pre+post)
	slices.Sort(routes)
	p.routes = slices.Compact(routes)
	return p
}

// cellsSets lists the instance sets of one node plan: one power mode and one
// Workload per set, at most limit instances, one variant per option, at most
// one CUDA instance, and the current instances excluded.
func cellsSets(pairs, current []cellsPair, limit int) [][]cellsPair {
	groups := map[[2]int][]cellsPair{}
	keys := [][2]int{}
	for _, c := range pairs {
		key := [2]int{c.opt.mode, c.w}
		if groups[key] == nil {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], c)
	}
	sets := [][]cellsPair{}
	for _, key := range keys {
		group := groups[key]
		for mask := 1; mask < 1<<len(group); mask++ {
			set := []cellsPair{}
			cuda, seen, twice := 0, map[string]bool{}, false
			for k, c := range group {
				if mask&(1<<k) != 0 {
					set = append(set, c)
					twice = twice || seen[c.opt.group]
					seen[c.opt.group] = true
					if c.opt.cuda {
						cuda++
					}
				}
			}
			same := len(set) == len(current) && !slices.ContainsFunc(set, func(c cellsPair) bool { return !cellsCurrent[c.id] })
			if len(set) <= limit && cuda <= 1 && !same && !twice {
				sets = append(sets, set)
			}
		}
	}
	return sets
}

// cellsUse sets the DP cell count and the plans' serve tables.
func cellsUse(p *capacityPlanner, cells int) {
	p.cells, p.paretoMax = cells, 0
	for w := range p.ws {
		p.ws[w].cells, p.ws[w].unit = cells, p.ws[w].rate/float64(cells)
	}
	for _, plans := range p.plans {
		for _, plan := range plans {
			p.serveTable(plan)
		}
	}
}

// cellsSolve runs the DP at the given cell count.
func cellsSolve(t testing.TB, p *capacityPlanner, cells int) (capacityResult, time.Duration) {
	cellsUse(p, cells)
	start := time.Now()
	best, err := p.solve()
	if err != nil {
		t.Error(err)
	}
	return best, time.Since(start)
}

// cellsCount is the instances and κ sum per Workload of a result.
func cellsCount(p *capacityPlanner, res *capacityResult) (int, []float64) {
	n, kappa := 0, make([]float64, len(p.ws))
	for node, j := range res.choice {
		for w, items := range p.plans[node][j].items {
			n += len(items)
			for _, item := range items {
				kappa[w] += item.kappa
			}
		}
	}
	return n, kappa
}

// §9 6 DP load cells: with r7 κ and costs and synthetic PLC and SmolVLM-like
// options (2–3 Workloads, 5 nodes), the DP choice at each cell count against
// the exact brute-force minimum. Each row is logged; the DP is never below the
// minimum and is feasible exactly when the brute force is. One three-Workload
// DP at 26 cells takes 9 to 24 minutes and the sweep about 80, so it runs on
// request: CHILL_DP_CELLS=1 go test ./internal/serving -run CellSize -v -timeout 0.
func TestCapacityCellSizeOverProvisioning(t *testing.T) {
	if os.Getenv("CHILL_DP_CELLS") == "" {
		t.Skip("measurement sweep; set CHILL_DP_CELLS=1")
	}
	type job struct {
		rates map[int]float64
		cells int
	}
	jobs := []job{}
	add := func(rates map[int]float64, cells ...int) {
		for _, c := range cells {
			jobs = append(jobs, job{rates, c})
		}
	}
	for _, mnv2 := range []float64{20, 60, 110} {
		for _, plc := range []float64{15, 40} {
			add(map[int]float64{0: mnv2, 1: plc}, 2, 5, 10, 26, 50)
			for _, vlm := range []float64{0.3, 1.2} {
				rates := map[int]float64{0: mnv2, 1: plc, 2: vlm}
				add(rates, 2, 5, 10)
				if mnv2 != 60 && plc == 40 {
					add(rates, 26) // the cell count of three Workloads; 9 to 24 min each
				}
			}
		}
		for _, vlm := range []float64{0.3, 1.2} {
			add(map[int]float64{0: mnv2, 2: vlm}, 2, 5, 10, 26, 50)
		}
	}
	run := func(j job) {
		names := []string{}
		for w, wl := range cellsWorkloads {
			if rate, ok := j.rates[w]; ok {
				names = append(names, fmt.Sprintf("%s=%g", wl.name, rate))
			}
		}
		p := cellsPlanner(j.rates, cellsNodes())
		start := time.Now()
		exact := capacityBruteForce(p)
		brute := time.Since(start)
		best, elapsed := cellsSolve(t, p, j.cells)
		if best.feasible != (exact != nil) {
			t.Errorf("%v cells %d: DP feasible=%v, brute force %v", names, j.cells, best.feasible, exact != nil)
			return
		}
		if exact == nil {
			t.Logf("ROW %s cells=%d infeasible dp=%v brute=%v", strings.Join(names, ","), j.cells, elapsed.Round(time.Millisecond), brute.Round(time.Millisecond))
			return
		}
		if best.joules < exact.joules-1e-6 {
			t.Errorf("%v cells %d: DP %g J below the exact minimum %g J", names, j.cells, best.joules, exact.joules)
		}
		n, kappa := cellsCount(p, &best)
		m, want := cellsCount(p, exact)
		for w := range kappa {
			kappa[w] = math.Round((kappa[w]-want[w])*10) / 10
		}
		t.Logf("ROW %s cells=%d exactJ=%.1f gapJ=%.1f differs=%v dInstances=%+d dKappa=%v dp=%v brute=%v pareto=%d\n  dp:    %s\n  exact: %s",
			strings.Join(names, ","), j.cells, exact.joules, best.joules-exact.joules, best.key != exact.key, n-m, kappa,
			elapsed.Round(time.Millisecond), brute.Round(time.Millisecond), p.paretoMax, best.key, exact.key)
	}
	// The rest three at a time, then the largest DP stages alone.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	big := []job{}
	for _, j := range jobs {
		if math.Pow(float64(j.cells+1), float64(len(j.rates))) > 10000 {
			big = append(big, j)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			run(j)
		}()
	}
	wg.Wait()
	for _, j := range big {
		run(j)
	}
}

// cellsLocal is a best-improvement search over plan sets from start (keep when
// nil): each step moves to the best plan set that changes at most k Nodes. It
// returns the result, the steps taken and the plan sets evaluated.
func cellsLocal(p *capacityPlanner, k int, start []int) (capacityResult, int, int) {
	choice := slices.Clone(start)
	if choice == nil {
		choice = make([]int, len(p.plans))
	}
	best := p.evaluate(slices.Clone(choice), p.routeOf(choice))
	steps, evals := 0, 0
	for {
		var step *capacityResult
		var walk func(from, left int, c []int)
		walk = func(from, left int, c []int) {
			for a := from; a < len(c); a++ {
				for j := range p.plans[a] {
					if j == choice[a] {
						continue
					}
					d := slices.Clone(c)
					d[a] = j
					evals++
					if res := p.evaluate(d, p.routeOf(d)); res.better(step) {
						step = &res
					}
					if left > 1 {
						walk(a+1, left-1, d)
					}
				}
			}
		}
		walk(0, k, choice)
		if step == nil || !step.better(&best) {
			return best, steps, evals
		}
		best, choice, steps = *step, step.choice, steps+1
	}
}

// cellsBrute is capacityBruteForce split over the plans of the first Node,
// eight at a time. The result's choice[0] is 0; key and joules are exact.
func cellsBrute(p *capacityPlanner) *capacityResult {
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
			results[j] = capacityBruteForce(&q)
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

// Plan search at E5 scale. The local search (k = 1, 2 Nodes per step from
// keep, then k = 3 from the k = 2 result) against the exact minimum on 24 load
// mixes, over the r7 nodes, the same nodes with b2 variants and 8 Nodes (b1, 2
// power modes on the Jetson copies). Then the plan counts, local-search time,
// exact time and DP time at 2 cells as cellsScaled grows the space toward E5
// (8 Nodes, 3 Workloads, b2 variants, up to 4 power modes on the Jetson
// copies). A DP past dpLimit is logged as a lower bound and the larger spaces
// skip it. CHILL_DP_CELLS=1 go test ./internal/serving -run PlanSearch -v -timeout 0
// (about 26 minutes).
func TestCapacityPlanSearch(t *testing.T) {
	if os.Getenv("CHILL_DP_CELLS") == "" {
		t.Skip("measurement sweep; set CHILL_DP_CELLS=1")
	}
	for _, c := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}} {
		nodes := cellsScaled(c.variants, c.modes)
		mixes := 0
		worst, differs, maxSteps, maxEvals := [4]float64{}, [4]int{}, [4]int{}, [4]int{}
		for _, mnv2 := range []float64{20, 60, 110} {
			for _, rates := range []map[int]float64{{0: mnv2, 1: 15}, {0: mnv2, 1: 40}, {0: mnv2, 2: 0.3}, {0: mnv2, 2: 1.2},
				{0: mnv2, 1: 15, 2: 0.3}, {0: mnv2, 1: 15, 2: 1.2}, {0: mnv2, 1: 40, 2: 0.3}, {0: mnv2, 1: 40, 2: 1.2}} {
				p := cellsPlanner(rates, nodes)
				exact := cellsBrute(p)
				mixes++
				var prev []int
				for k := 1; k <= 3; k++ {
					local, steps, evals := cellsLocal(p, k, prev)
					if prev = nil; k == 2 {
						prev = local.choice
					}
					maxSteps[k], maxEvals[k] = max(maxSteps[k], steps), max(maxEvals[k], evals)
					if (exact != nil) != local.feasible {
						t.Errorf("%+v k %d rates %v: local feasible=%v, exact %v", c, k, rates, local.feasible, exact != nil)
						continue
					}
					if exact == nil {
						continue
					}
					gap := (local.joules - exact.joules) / exact.joules
					if local.key != exact.key {
						differs[k]++
						t.Logf("%+v k=%d %v gapJ=%.1f (%.2f%%)\n  local: %s\n  exact: %s", c, k, rates,
							local.joules-exact.joules, 100*gap, local.key, exact.key)
					}
					worst[k] = max(worst[k], gap)
				}
			}
		}
		for k := 1; k <= 3; k++ {
			t.Logf("LOCAL variants=%d modes=%d k=%d mixes=%d differs=%d worstGap=%.3f%% steps<=%d evals<=%d",
				c.variants, c.modes, k, mixes, differs[k], 100*worst[k], maxSteps[k], maxEvals[k])
		}
	}
	const dpLimit = 5 * time.Minute
	dpOver := false
	rates := map[int]float64{0: 60, 1: 40, 2: 1.2}
	for _, c := range []struct{ variants, modes int }{{1, 0}, {2, 0}, {1, 2}, {1, 4}, {2, 1}, {2, 2}, {2, 4}} {
		p := cellsPlanner(rates, cellsScaled(c.variants, c.modes))
		cellsUse(p, 2)
		counts, total, front, sets, pruned := []int{}, 0, 0, 1.0, 1.0
		for n, plans := range p.plans {
			f := len(p.frontier(n, p.routes[len(p.routes)-1]))
			counts, total, front = append(counts, len(plans)), total+len(plans), front+f
			sets, pruned = sets*float64(len(plans)), pruned*float64(f)
		}
		local, prev := [4]string{}, []int(nil)
		for k := 1; k <= 3; k++ {
			start := time.Now()
			res, steps, evals := cellsLocal(p, k, prev)
			elapsed := time.Since(start)
			if prev = nil; k == 2 {
				prev = res.choice
			}
			local[k] = fmt.Sprintf("%v %d steps %d evals (%.1f µs each) %.1f J", elapsed.Round(time.Millisecond), steps, evals,
				float64(elapsed.Microseconds())/float64(evals), res.joules)
		}
		exact := "skipped"
		if sets <= 2e8 {
			start := time.Now()
			if res := cellsBrute(p); res != nil {
				exact = fmt.Sprintf("%v %.1f J", time.Since(start).Round(time.Millisecond), res.joules)
			}
		}
		dp := "skipped"
		if !dpOver {
			ctx, cancel := context.WithCancel(context.Background())
			p.ctx = ctx
			done := make(chan string, 1)
			go func() {
				start := time.Now()
				res, err := p.solve()
				if err == nil {
					done <- fmt.Sprintf("%v %.1f J pareto=%d", time.Since(start).Round(time.Millisecond), res.joules, p.paretoMax)
				}
			}()
			select {
			case dp = <-done:
			case <-time.After(dpLimit):
				dp, dpOver = fmt.Sprintf(">%v", dpLimit), true
			}
			cancel() // an unfinished DP stops at its next route time
		}
		t.Logf("SCALE variants=%d modes=%d plans=%v sum=%d frontier=%d sets=%.3g frontierSets=%.3g routes=%d\n  dp(2 cells) %s\n  exact %s\n  local k=1 %s\n  local k=2 %s\n  then k=3 %s",
			c.variants, c.modes, counts, total, front, sets, pruned, len(p.routes), dp, exact, local[1], local[2], local[3])
	}
}
