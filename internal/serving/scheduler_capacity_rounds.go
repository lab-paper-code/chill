package serving

import (
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler"
	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// capacityRoundsExact is the v2 exact port of one decision. Over capacityExact
// it gathers the supply ratios of replay shortfalls and records each plan's
// judgment once across the decision's candidate generations (rounds). The
// observer slots hold the current round; a reset (v1 fallback) clears only
// them, and endRound moves them into the decision's earlier observations.
// Observer slots stay private to each exact worker.
type capacityRoundsExact struct {
	*capacityExact
	ratios   []map[int]float64 // per slot: least shortfall/profile throughput ratio by Workload
	judged   map[string]bool   // plans judged in earlier rounds; read-only during a round
	current  []map[string]bool // per slot: plans judged in this round
	rejudged []int             // per slot: judgments of plans judged in an earlier round
	// Exclusion counts and reserve rejections of earlier rounds.
	earlierExcluded map[string]int
	earlierReserves map[string]SchedulerTransitionReserve
}

func newCapacityRoundsExact(e *capacityExact) *capacityRoundsExact {
	workers := len(e.excluded)
	r := &capacityRoundsExact{capacityExact: e, ratios: make([]map[int]float64, workers), judged: map[string]bool{},
		current: make([]map[string]bool, workers), rejudged: make([]int, workers), earlierExcluded: map[string]int{}}
	for w := range workers {
		r.ratios[w], r.current[w] = map[int]float64{}, map[string]bool{}
	}
	return r
}

func (e *capacityRoundsExact) ResetObservations() {
	e.capacityExact.ResetObservations()
	for w := range e.current {
		clear(e.current[w])
		e.rejudged[w] = 0
	}
}

func (e *capacityRoundsExact) Observe(r capacityResult, w int) {
	plan := fmt.Sprint(r.choice)
	if e.judged[plan] {
		e.rejudged[w]++
		return
	}
	e.current[w][plan] = true
	e.capacityExact.Observe(r, w)
	e.p.supplyRatios(r, e.ratios[w])
}

// endRound marks this round's plans judged, moves its observations into the
// earlier ones and returns how many judgments repeated an earlier round's.
func (e *capacityRoundsExact) endRound() int {
	rejudged := 0
	for w := range e.current {
		for plan := range e.current[w] {
			e.judged[plan] = true
		}
		clear(e.current[w])
		rejudged += e.rejudged[w]
		e.rejudged[w] = 0
		for reason, count := range e.excluded[w] {
			e.earlierExcluded[reason] += count
		}
		for _, record := range e.reserves[w] {
			e.earlierReserves = reserveRejections(e.earlierReserves, []SchedulerTransitionReserve{record}, "")
		}
	}
	e.capacityExact.ResetObservations()
	return rejudged
}

// record records the search with every round's observations; endRound has
// moved them all out of the observer slots.
func (e *capacityRoundsExact) record(d protocol.Diagnostics) {
	e.excluded[0], e.reserves[0] = e.earlierExcluded, e.earlierReserves
	e.capacityExact.record(d)
}

// supplyRatios records, for each shortfall of res, the measured completed rate
// over the uncorrected profile throughput res routes to that Workload, keeping
// the least ratio per Workload. A plan without profile throughput for w has no
// throughput to correct.
func (p *capacityPlanner) supplyRatios(res capacityResult, into map[int]float64) {
	for _, s := range p.shortfalls(res) {
		profile := 0.0
		for n, j := range res.choice {
			if plan := p.plans[n][j]; s.w < len(plan.items) {
				for _, item := range plan.items[s.w] {
					profile += item.throughput
				}
			}
		}
		if profile == 0 {
			continue
		}
		if old, ok := into[s.w]; !ok || s.completed/profile < old {
			into[s.w] = s.completed / profile
		}
	}
}

// lowerSupply folds the gathered ratios into the planner's correction c_w,
// which only falls and only applies below 1, and reports whether any fell.
func (e *capacityRoundsExact) lowerSupply() bool {
	lowered := false
	for _, ratios := range e.ratios {
		for w, ratio := range ratios {
			if current, ok := e.p.supplyCorrection[w]; ratio >= 1 || ok && ratio >= current {
				continue
			}
			if e.p.supplyCorrection == nil {
				e.p.supplyCorrection = map[int]float64{}
			}
			e.p.supplyCorrection[w] = ratio
			lowered = true
		}
	}
	return lowered
}

// capacityRounds generates candidates and regenerates them while the best exact
// result misses an SLO and lower reports that some c_w fell. A regenerated
// round replaces the best only when its exact result is strictly better under
// the exact order, and a round that is not better ends the loop. Termination:
// every round after the first starts only once c_w has fallen (c_w only falls)
// and continues the loop only by strictly improving the best exact result, and
// a decision has finitely many plans to improve through.
func capacityRounds(run func() (protocol.Outcome[capacityResult], error), lower func() bool) (protocol.Outcome[capacityResult], int, error) {
	best, err := run()
	selected := 0
	for round := 1; err == nil && !best.Value.feasible && lower(); round++ {
		next, nextErr := run()
		if nextErr != nil {
			return best, selected, nextErr
		}
		if !next.Found || !next.Value.better(&best.Value) {
			break
		}
		best, selected = next, round
	}
	return best, selected, err
}

// searchRounds runs the v2 engine over scalar tables whose supply carries the
// replay correction c_w (planTable). Each round's traversal receives the first
// round's budget: the evaluation count and the time from search start to the
// deadline, counted from the round's start.
func (p *capacityPlanner) searchRounds(base *capacityExact, started, deadline time.Time) (capacityResult, error) {
	e := newCapacityRoundsExact(base)
	engine := scheduler.Select[capacityResult](protocol.V2)
	// The engine's first act in every round is this keep evaluation, in this
	// planner state before SeedKeep; its replay is memoized. Reading it first
	// lets keep's shortfalls correct the first table.
	keepStarted := time.Now()
	p.supplyRatios(e.Evaluate(make([]int, len(p.plans))), e.ratios[0])
	keepElapsed := time.Since(keepStarted)
	e.lowerSupply()
	budget, roundStarted := deadline.Sub(started), started
	var rounds []protocol.Diagnostics
	run := func() (protocol.Outcome[capacityResult], error) {
		roundDeadline := deadline // a zero deadline disables wall-clock cuts in every round
		if !deadline.IsZero() {
			roundDeadline = roundStarted.Add(budget)
		}
		result, err := engine.Search(p.scalarProblem(p.planTable(), roundDeadline), e)
		// v2 counts exact judgments in both counters; its v1 fallback in Evaluated only.
		d := &result.Diagnostics
		d.Evaluated -= e.endRound()
		d.ExactEvaluated = min(d.ExactEvaluated, d.Evaluated)
		rounds = append(rounds, *d)
		roundStarted = time.Now()
		return result, err
	}
	best, selected, err := capacityRounds(run, e.lowerSupply)
	d := mergeSearchRounds(rounds, selected)
	d.ExactElapsed += keepElapsed
	p.recordSearch(e, d, started)
	step := 0
	for round, r := range rounds {
		for range r.Steps {
			p.searched.Steps[step].Round = round + 1
			step++
		}
	}
	p.searched.Rounds = len(rounds)
	for w, c := range p.supplyCorrection {
		if p.searched.SupplyCorrection == nil {
			p.searched.SupplyCorrection = map[string]float64{}
		}
		p.searched.SupplyCorrection[p.ws[w].d.UID] = c
	}
	return best.Value, err
}

// mergeSearchRounds records the rounds of one search: work, time and steps add
// up; the last round, which ended the search, gives the cut flag and the
// termination reason; the selected round names the result, its funnel and its
// finalists.
func mergeSearchRounds(rounds []protocol.Diagnostics, selected int) protocol.Diagnostics {
	d := rounds[selected]
	last := rounds[len(rounds)-1]
	d.BudgetCut, d.TerminationReason = last.BudgetCut, last.TerminationReason
	d.Evaluated, d.ExactEvaluated, d.SurrogateEvaluated, d.TrailingEvaluated = 0, 0, 0, 0
	d.SurrogateElapsed, d.ExactElapsed, d.Steps = 0, 0, nil
	for _, r := range rounds {
		d.Evaluated += r.Evaluated
		d.ExactEvaluated += r.ExactEvaluated
		d.SurrogateEvaluated += r.SurrogateEvaluated
		d.TrailingEvaluated += r.TrailingEvaluated
		d.SurrogateElapsed += r.SurrogateElapsed
		d.ExactElapsed += r.ExactElapsed
		d.Steps = append(d.Steps, r.Steps...)
	}
	return d
}
