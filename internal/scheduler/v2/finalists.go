package v2

import (
	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
	"slices"
	"time"
)

type CoreStats struct {
	SurrogateEvaluated int
	Deadline           bool
	Exhausted          bool
	steps              int
	SearchSteps        []protocol.Step
	DualBound          float64
}

type v2Finalist struct {
	key   protocol.Rank
	Start string
}

// Sorted pointers rotate on insertion; only an admitted candidate is copied.
type v2Finalists struct {
	ordered []*v2Finalist
	used    int
}

func newV2Key(t *protocol.Table, workloads int) protocol.Rank {
	return protocol.Rank{Table: t, Choice: make([]int, len(t.ByPlan)), Excesses: make([]float64, workloads),
		Demand: make([]float64, workloads), Supply: make([]float64, workloads), Satisfied: make([]bool, workloads)}
}

func newV2Finalists(t *protocol.Table, workloads, size int) v2Finalists {
	f := v2Finalists{ordered: make([]*v2Finalist, max(1, size))}
	for i := range f.ordered {
		f.ordered[i] = &v2Finalist{key: newV2Key(t, workloads)}
	}
	return f
}

func (f *v2Finalists) add(key protocol.Rank, start string) {
	if f.used == len(f.ordered) && !key.Better(&f.ordered[f.used-1].key) {
		return
	}
	at := 0
	for at < f.used && !key.Better(&f.ordered[at].key) {
		if slices.Equal(key.Choice, f.ordered[at].key.Choice) {
			return
		}
		at++
	}
	if at < f.used && slices.Equal(key.Choice, f.ordered[at].key.Choice) {
		return
	}
	last := min(f.used, len(f.ordered)-1)
	slot := f.ordered[last]
	copy(f.ordered[at+1:last+1], f.ordered[at:last])
	f.ordered[at] = slot
	key.CopyInto(&slot.key)
	slot.Start = start
	f.used = min(f.used+1, len(f.ordered))
}

type v2Search struct {
	s                         *state
	problem                   protocol.Problem
	current, best             protocol.Rank
	finalists                 v2Finalists
	stats                     CoreStats
	limit, active             int
	Deadline                  time.Time
	Start                     string
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

func newV2Search(p protocol.Problem, t *protocol.Table, choice []int, evaluations, finalists, active int, deadline time.Time) *v2Search {
	allActive := active <= 0
	if allActive {
		for _, plans := range p.Plans {
			active = max(active, len(plans))
		}
	}
	c := &v2Search{problem: p, s: newState(p, t, choice), current: newV2Key(t, len(p.Workloads)), best: newV2Key(t, len(p.Workloads)),
		finalists: newV2Finalists(t, len(p.Workloads), finalists), limit: evaluations, active: active, Deadline: deadline,
		moves: make([][]int, len(p.Plans)), ranked: make([][]v2Merit, len(p.Plans)), activePlans: make([][]int, len(p.Plans)),
		suffix: make([][]float64, len(p.Plans)+1), optimistic: make([]float64, len(p.Workloads)), allActive: allActive,
		suffixEnergy: make([]float64, len(p.Plans)+1), suffixRoute: make([]float64, len(p.Plans)+1)}
	largest := 0
	for n := range p.Plans {
		c.moves[n] = make([]int, 0, len(p.Plans[n]))
		c.ranked[n] = make([]v2Merit, 0, min(c.active, len(p.Plans[n])))
		c.activePlans[n] = make([]int, 0, min(c.active+2, len(p.Plans[n])))
		largest = max(largest, len(p.Plans[n]))
	}
	c.second = make([]int, 0, largest)
	for i := range c.suffix {
		c.suffix[i] = make([]float64, len(p.Workloads))
	}
	return c
}

func (c *v2Search) stopped() bool {
	return c.stats.Deadline || c.stats.Exhausted
}

func (c *v2Search) expired() bool {
	if c.stopped() {
		return true
	}
	if !c.stats.Deadline && !c.Deadline.IsZero() && time.Now().After(c.Deadline) {
		c.stats.Deadline = true
	}
	return c.stopped()
}

func (c *v2Search) evaluate(n, q int) (protocol.Rank, bool) {
	if c.stopped() {
		return protocol.Rank{}, false
	}
	if c.limit > 0 && c.stats.SurrogateEvaluated >= c.limit {
		c.stats.Exhausted = true
		return protocol.Rank{}, false
	}
	c.stats.SurrogateEvaluated++
	return c.s.delta(n, q), true
}

func (c *v2Search) consider(key protocol.Rank) {
	if key.Bounded && key.Searchable && !key.UnroutedPrepare {
		c.finalists.add(key, c.Start)
		if key.Better(&c.best) {
			key.CopyInto(&c.best)
		}
	}
}

// recordStep copies only accepted points and origins, never evaluated candidates.
func (c *v2Search) recordStep(key protocol.Rank, previous []int, phase string, evaluated int, started time.Time) {
	step := protocol.Step{Start: c.Start, Phase: phase, Point: protocol.Point{Joules: key.Joules, KeepJoules: key.KeepJoules, Period: key.Period, Feasible: key.Feasible, Final: key.Final}, Evaluated: evaluated, Duration: time.Since(started)}
	if previous != nil {
		for n, q := range key.Choice {
			if q != previous[n] {
				step.K++
			}
		}
		step.ChangedNodes = make([]int, 0, step.K)
		for n, q := range key.Choice {
			if q != previous[n] {
				step.ChangedNodes = append(step.ChangedNodes, n)
			}
		}
	}
	c.stats.SearchSteps = append(c.stats.SearchSteps, step)
}

// Candidates runs deterministic keep, empty and optional price-guided starts.
// Endpoints are retained outside K,
// including a partially searched endpoint when either budget expires.
// active<=0 includes every searchable plan, including every cap variant.
// evaluations<=0 disables the evaluation limit for exhaustive harness runs.
func Candidates(p protocol.Problem, t *protocol.Table, evaluations, finalists, active int, deadline time.Time) ([]protocol.Candidate, CoreStats) {
	Group(t)
	if len(p.Plans) == 0 {
		return nil, CoreStats{}
	}
	keep, empty := make([]int, len(p.Plans)), make([]int, len(p.Plans))
	for n := range keep {
		keep[n], empty[n] = t.ByPlan[n][0], t.ByPlan[n][0]
		for _, e := range t.EntryOrder[n] {
			for _, q := range t.Entries[e].Variants {
				count := 0
				for _, v := range t.Count[q] {
					count += v
				}
				if count == 0 && t.Searchable[q] {
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
		Choice []int
	}{{"keep", keep}}
	if !slices.Equal(keep, empty) {
		starts = append(starts, struct {
			name   string
			Choice []int
		}{"empty", empty})
	}
	c := newV2Search(p, t, keep, evaluations, finalists, active, deadline)
	if p.Priced {
		priced, dual := PricedStart(p, t)
		c.stats.DualBound = dual
		if priced != nil && !slices.Equal(priced, keep) && !slices.Equal(priced, empty) {
			starts = append(starts, struct {
				name   string
				Choice []int
			}{"priced", priced})
		}
	}
	endpoints := make([]v2Finalist, 0, len(starts))
	for _, start := range starts {
		if c.expired() {
			break
		}
		c.Start = start.name
		c.s.reset(start.Choice)
		stepStart := time.Now()
		key, ok := c.evaluate(0, c.s.Choice[0])
		if !ok {
			break
		}
		key.CopyInto(&c.current)
		c.recordStep(c.current, nil, "", 0, stepStart)
		c.finalists.add(c.current, c.Start)
		c.cover()
		c.polish()
		endpoint := v2Finalist{key: newV2Key(t, len(p.Workloads)), Start: c.Start}
		c.current.CopyInto(&endpoint.key)
		endpoints = append(endpoints, endpoint)
	}
	all := append([]*v2Finalist(nil), c.finalists.ordered[:c.finalists.used]...)
	for i := range endpoints {
		if !slices.ContainsFunc(all, func(f *v2Finalist) bool { return slices.Equal(f.key.Choice, endpoints[i].key.Choice) }) {
			all = append(all, &endpoints[i])
		}
	}
	slices.SortFunc(all, func(a, b *v2Finalist) int {
		if a.key.Better(&b.key) {
			return -1
		}
		if b.key.Better(&a.key) {
			return 1
		}
		return 0
	})
	out := make([]protocol.Candidate, len(all))
	for i, finalist := range all {
		choice := make([]int, len(p.Plans))
		for n, q := range finalist.key.Choice {
			choice[n] = t.Index[q]
		}
		out[i] = protocol.Candidate{Choice: choice, Joules: finalist.key.Joules, Feasible: finalist.key.Feasible, Start: finalist.Start}
	}
	return out, c.stats
}
