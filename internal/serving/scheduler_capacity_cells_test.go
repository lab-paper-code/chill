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
}

// cellsNodes are the five nodes m4 r7 planned on (Jetsons excluded), with the
// unloaded node baseline and the measured remove chain of each node.
func cellsNodes() [][]cellsOption {
	node := func(name string, unloaded, removeS, removeJ float64, limit int, options ...cellsOption) []cellsOption {
		for k := range options {
			options[k].node, options[k].unloaded, options[k].removeS, options[k].removeJ, options[k].limit = name, unloaded, removeS, removeJ, limit
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
// node every set of at most limit instances of one model with at most one
// CUDA instance (nodePlans holds one model per node plan); plans[n][0] keeps
// the current instances. Removals run after the route.
func cellsPlanner(rates map[int]float64) *capacityPlanner {
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
		for _, options := range cellsNodes() {
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
	for _, options := range cellsNodes() {
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
		for mask := 1; mask < 1<<len(pairs); mask++ {
			set := []cellsPair{}
			cuda := 0
			for k, c := range pairs {
				if mask&(1<<k) != 0 {
					set = append(set, c)
					if c.opt.cuda {
						cuda++
					}
				}
			}
			same := len(set) == len(current) && !slices.ContainsFunc(set, func(c cellsPair) bool { return !cellsCurrent[c.id] })
			mixed := slices.ContainsFunc(set, func(c cellsPair) bool { return c.w != set[0].w })
			if len(set) <= options[0].limit && cuda <= 1 && !same && !mixed {
				plans = append(plans, plan(set, false))
			}
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

// cellsSolve runs the DP at the given cell count.
func cellsSolve(t testing.TB, p *capacityPlanner, cells int) (capacityResult, time.Duration) {
	p.cells, p.paretoMax = cells, 0
	for w := range p.ws {
		p.ws[w].cells, p.ws[w].unit = cells, p.ws[w].rate/float64(cells)
	}
	for _, plans := range p.plans {
		for _, plan := range plans {
			p.serveTable(plan)
		}
	}
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
		p := cellsPlanner(j.rates)
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
