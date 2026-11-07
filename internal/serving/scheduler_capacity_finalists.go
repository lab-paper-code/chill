package serving

import (
	"slices"
	"time"
)

// v2Candidate owns v1 plan indices, ready for the exact admission funnel.
type v2Candidate struct {
	choice   []int
	joules   float64
	feasible bool
	start    string
}

type v2CoreStats struct {
	surrogateEvaluated int
	budgetCut          bool
	steps              int
	searchSteps        []SchedulerPlanSearchStep
	dualBound          float64
}

type v2Finalist struct {
	key   orderKey
	start string
}

// Sorted pointers rotate on insertion; only an admitted candidate is copied.
type v2Finalists struct {
	ordered []*v2Finalist
	used    int
}

func newV2Key(t *planTable, workloads int) orderKey {
	return orderKey{table: t, choice: make([]int, len(t.byPlan)), excesses: make([]float64, workloads),
		demand: make([]float64, workloads), supply: make([]float64, workloads), satisfied: make([]bool, workloads)}
}

func newV2Finalists(t *planTable, workloads, size int) v2Finalists {
	f := v2Finalists{ordered: make([]*v2Finalist, max(1, size))}
	for i := range f.ordered {
		f.ordered[i] = &v2Finalist{key: newV2Key(t, workloads)}
	}
	return f
}

func (f *v2Finalists) add(key orderKey, start string) {
	if f.used == len(f.ordered) && !key.better(&f.ordered[f.used-1].key) {
		return
	}
	at := 0
	for at < f.used && !key.better(&f.ordered[at].key) {
		if slices.Equal(key.choice, f.ordered[at].key.choice) {
			return
		}
		at++
	}
	if at < f.used && slices.Equal(key.choice, f.ordered[at].key.choice) {
		return
	}
	last := min(f.used, len(f.ordered)-1)
	slot := f.ordered[last]
	copy(f.ordered[at+1:last+1], f.ordered[at:last])
	f.ordered[at] = slot
	key.copyInto(&slot.key)
	slot.start = start
	f.used = min(f.used+1, len(f.ordered))
}

type v2Search struct {
	s                         *searchState
	current, best             orderKey
	finalists                 v2Finalists
	stats                     v2CoreStats
	limit, active             int
	deadline                  time.Time
	start                     string
	moves                     [][]int
	second                    []int
	ranked                    [][]v2Merit
	activePlans               [][]int
	suffix                    [][]float64
	optimistic                []float64
	allActive                 bool
	suffixEnergy, suffixRoute []float64
	energyBoundValid          bool
}

func newV2Search(p *capacityPlanner, t *planTable, choice []int, evaluations, finalists, active int, deadline time.Time) *v2Search {
	allActive := active <= 0
	if allActive {
		for _, plans := range p.plans {
			active = max(active, len(plans))
		}
	}
	c := &v2Search{s: newSearchState(p, t, choice), current: newV2Key(t, len(p.ws)), best: newV2Key(t, len(p.ws)),
		finalists: newV2Finalists(t, len(p.ws), finalists), limit: evaluations, active: active, deadline: deadline,
		moves: make([][]int, len(p.plans)), ranked: make([][]v2Merit, len(p.plans)), activePlans: make([][]int, len(p.plans)),
		suffix: make([][]float64, len(p.plans)+1), optimistic: make([]float64, len(p.ws)), allActive: allActive,
		suffixEnergy: make([]float64, len(p.plans)+1), suffixRoute: make([]float64, len(p.plans)+1)}
	largest := 0
	for n := range p.plans {
		c.moves[n] = make([]int, 0, len(p.plans[n]))
		c.ranked[n] = make([]v2Merit, 0, min(c.active, len(p.plans[n])))
		c.activePlans[n] = make([]int, 0, min(c.active+2, len(p.plans[n])))
		largest = max(largest, len(p.plans[n]))
	}
	c.second = make([]int, 0, largest)
	for i := range c.suffix {
		c.suffix[i] = make([]float64, len(p.ws))
	}
	return c
}

func (c *v2Search) expired() bool {
	if c.stats.budgetCut {
		return true
	}
	if !c.deadline.IsZero() && time.Now().After(c.deadline) {
		c.stats.budgetCut = true
	}
	return c.stats.budgetCut
}

func (c *v2Search) evaluate(n, q int) (orderKey, bool) {
	if c.stats.budgetCut || c.limit > 0 && c.stats.surrogateEvaluated >= c.limit {
		c.stats.budgetCut = true
		return orderKey{}, false
	}
	c.stats.surrogateEvaluated++
	return c.s.delta(n, q), true
}

func (c *v2Search) consider(key orderKey) {
	if key.bounded && key.searchable && !key.unroutedPrepare {
		c.finalists.add(key, c.start)
		if key.better(&c.best) {
			key.copyInto(&c.best)
		}
	}
}

// recordStep copies only accepted points and origins, never evaluated candidates.
func (c *v2Search) recordStep(key orderKey, previous []int, phase string, evaluated int, started time.Time) {
	step := SchedulerPlanSearchStep{
		Start: c.start, Phase: phase, Joules: key.joules, KeepJoules: key.keepJoules,
		ComparisonWindow: time.Duration(key.period * 1e9), Feasible: key.feasible, Final: key.final,
		Evaluated: evaluated, Duration: time.Since(started),
	}
	if previous != nil {
		for n, q := range key.choice {
			if q != previous[n] {
				step.K++
			}
		}
		step.ChangedNodes = make([]NodeReference, 0, step.K)
		for n, q := range key.choice {
			if q != previous[n] {
				step.ChangedNodes = append(step.ChangedNodes, c.s.planner.snapshot.Nodes[n].Node)
			}
		}
	}
	c.stats.searchSteps = append(c.stats.searchSteps, step)
}

// searchV2Core runs deterministic keep, empty and optional price-guided starts.
// Endpoints are retained outside K,
// including a partially searched endpoint when either budget expires.
// active<=0 includes every searchable plan, including every cap variant.
// evaluations<=0 disables the evaluation limit for exhaustive harness runs.
func (p *capacityPlanner) searchV2Core(t *planTable, evaluations, finalists, active int, deadline time.Time) ([]v2Candidate, v2CoreStats) {
	if len(p.plans) == 0 {
		return nil, v2CoreStats{}
	}
	t.initSearch(p)
	keep, empty := make([]int, len(p.plans)), make([]int, len(p.plans))
	for n := range keep {
		keep[n], empty[n] = t.byPlan[n][0], t.byPlan[n][0]
		for _, e := range t.entryOrder[n] {
			for _, q := range t.entries[e].variants {
				count := 0
				for _, v := range t.count[q] {
					count += v
				}
				if count == 0 && t.searchable[q] {
					empty[n] = q
					break
				}
			}
			if empty[n] != keep[n] {
				break
			}
		}
	}
	starts := []struct {
		name   string
		choice []int
	}{{"keep", keep}}
	if !slices.Equal(keep, empty) {
		starts = append(starts, struct {
			name   string
			choice []int
		}{"empty", empty})
	}
	c := newV2Search(p, t, keep, evaluations, finalists, active, deadline)
	if p.settings.PlanPricedStart {
		priced, dual := p.pricedStart(t)
		c.stats.dualBound = dual
		if priced != nil && !slices.Equal(priced, keep) && !slices.Equal(priced, empty) {
			starts = append(starts, struct {
				name   string
				choice []int
			}{"priced", priced})
		}
	}
	endpoints := make([]v2Finalist, 0, len(starts))
	for _, start := range starts {
		if c.expired() {
			break
		}
		c.start = start.name
		c.s.reset(start.choice)
		stepStart := time.Now()
		key, ok := c.evaluate(0, c.s.choice[0])
		if !ok {
			break
		}
		key.copyInto(&c.current)
		c.recordStep(c.current, nil, "", 0, stepStart)
		c.finalists.add(c.current, c.start)
		c.cover()
		c.polish()
		endpoint := v2Finalist{key: newV2Key(t, len(p.ws)), start: c.start}
		c.current.copyInto(&endpoint.key)
		endpoints = append(endpoints, endpoint)
	}
	all := append([]*v2Finalist(nil), c.finalists.ordered[:c.finalists.used]...)
	for i := range endpoints {
		if !slices.ContainsFunc(all, func(f *v2Finalist) bool { return slices.Equal(f.key.choice, endpoints[i].key.choice) }) {
			all = append(all, &endpoints[i])
		}
	}
	slices.SortFunc(all, func(a, b *v2Finalist) int {
		if a.key.better(&b.key) {
			return -1
		}
		if b.key.better(&a.key) {
			return 1
		}
		return 0
	})
	out := make([]v2Candidate, len(all))
	for i, finalist := range all {
		choice := make([]int, len(p.plans))
		for n, q := range finalist.key.choice {
			choice[n] = t.index[q]
		}
		out[i] = v2Candidate{choice: choice, joules: finalist.key.joules, feasible: finalist.key.feasible, start: finalist.start}
	}
	return out, c.stats
}
