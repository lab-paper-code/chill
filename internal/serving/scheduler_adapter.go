package serving

import (
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/scheduler"
	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// capacityExact keeps lifecycle admission, reserve evidence and native results in CHILL.
// Observer slots are private to each v1 worker; v2 uses slot zero.
type capacityExact struct {
	p        *capacityPlanner
	excluded []map[string]int
	reserves []map[string]SchedulerTransitionReserve
}

func newCapacityExact(p *capacityPlanner) *capacityExact {
	e := &capacityExact{p: p, excluded: make([]map[string]int, p.settings.PlanSearchWorkers), reserves: make([]map[string]SchedulerTransitionReserve, p.settings.PlanSearchWorkers)}
	for w := range e.excluded {
		e.excluded[w] = map[string]int{}
	}
	return e
}
func (e *capacityExact) ResetObservations() {
	for w := range e.excluded {
		clear(e.excluded[w])
		e.reserves[w] = nil
	}
}
func (e *capacityExact) Evaluate(choice []int) capacityResult {
	return e.p.evaluate(choice, e.p.routeOf(choice))
}
func (e *capacityExact) Bounded(choice []int) bool                       { return e.p.bounded(choice) }
func (e *capacityExact) Admits(r capacityResult) bool                    { return e.p.admits(&r) }
func (e *capacityExact) Better(a capacityResult, b *capacityResult) bool { return a.better(b) }
func (e *capacityExact) Choice(r capacityResult) []int                   { return r.choice }
func (e *capacityExact) Point(r capacityResult) protocol.Point {
	return protocol.Point{Key: r.key, Joules: r.joules, KeepJoules: r.keepJoules, Period: r.period, Feasible: r.feasible, Final: r.final}
}
func (e *capacityExact) SeedKeep(r capacityResult) { e.p.keepWatts = r.joules / r.period }
func (e *capacityExact) PostFilter(r capacityResult) capacityResult {
	return e.p.minimumGain(e.p.payback(r))
}
func (e *capacityExact) FilterKept(r capacityResult) bool { return capacityFilterKept(r) }
func (e *capacityExact) Observe(r capacityResult, w int) {
	e.reserves[w] = reserveRejections(e.reserves[w], r.reserves, r.key)
	if r.exclusion != "" {
		e.excluded[w][r.exclusion]++
	}
}

func capacityFilterKept(res capacityResult) bool {
	return res.keep || res.withinUncertainty || len(res.unpaid) > 0 && slices.IndexFunc(res.choice, func(j int) bool { return j != 0 }) < 0
}
func (e *capacityExact) record(d protocol.Diagnostics) {
	p := e.p
	p.searched = SchedulerPlanSearch{Strategy: d.Strategy, Neighborhood: d.Neighborhood, Evaluated: d.Evaluated,
		StartedAt: d.StartedAt, FinishedAt: d.FinishedAt, Duration: d.Duration, BudgetCut: d.BudgetCut,
		TrailingEvaluated: d.TrailingEvaluated, TerminationReason: d.TerminationReason, Key: d.Key, Joules: d.Joules,
		Pruned: d.Pruned, SurrogateEvaluated: d.SurrogateEvaluated, Finalists: d.Finalists, ExactEvaluated: d.ExactEvaluated,
		SurrogateGapJoules: d.SurrogateGapJoules, PostFilterReverts: d.PostFilterReverts, Fallback: d.Fallback, Start: d.Start, DualBound: d.DualBound,
	}
	if d.Strategy == protocol.V1 || d.Fallback == protocol.V1 {
		p.searched.Excluded = map[string]int{}
	}
	for _, counts := range e.excluded {
		for reason, count := range counts {
			if p.searched.Excluded == nil {
				p.searched.Excluded = map[string]int{}
			}
			p.searched.Excluded[reason] += count
		}
	}
	for _, records := range e.reserves {
		for _, record := range records {
			p.searched.ReserveRejections = reserveRejections(p.searched.ReserveRejections, []SchedulerTransitionReserve{record}, "")
		}
	}
	for _, step := range d.Steps {
		record := SchedulerPlanSearchStep{Start: step.Start, Phase: step.Phase, K: step.K, Joules: step.Joules, KeepJoules: step.KeepJoules,
			ComparisonWindow: time.Duration(step.Period * 1e9), Feasible: step.Feasible, Final: step.Final, Evaluated: step.Evaluated, Duration: step.Duration, BudgetCut: step.BudgetCut}
		if step.ChangedNodes != nil {
			record.ChangedNodes = make([]NodeReference, 0, len(step.ChangedNodes))
			for _, n := range step.ChangedNodes {
				record.ChangedNodes = append(record.ChangedNodes, p.snapshot.Nodes[n].Node)
			}
		}
		p.searched.Steps = append(p.searched.Steps, record)
	}
}

func (p *capacityPlanner) searchProblem(deadline time.Time) protocol.Problem {
	problem := protocol.Problem{Context: p.ctx, Workers: p.settings.PlanSearchWorkers, Deadline: deadline,
		Evaluations: p.settings.PlanSearchEvaluations, Finalists: p.settings.PlanExactFinalists, Active: p.settings.PlanPolishActive,
		Priced: p.settings.PlanPricedStart, PostFilter: p.settings.PlanPostFilterFinalists, Plans: make([][]protocol.Plan, len(p.plans)), Workloads: make([]protocol.Workload, len(p.ws))}
	for n, plans := range p.plans {
		problem.Plans[n] = make([]protocol.Plan, len(plans))
		for j, plan := range plans {
			problem.Plans[n][j] = protocol.Plan{Eligible: plan.record.Eligible, Empty: len(plan.instances) == 0}
		}
	}
	for w, ws := range p.ws {
		problem.Workloads[w] = protocol.Workload{Rate: ws.rate, Queued: ws.queued, Slack: ws.slack}
	}
	return problem
}

func (p *capacityPlanner) search(deadline time.Time) (capacityResult, error) {
	started := time.Now()
	e := newCapacityExact(p)
	var problem protocol.Problem
	if p.settings.PlanSearchStrategy == protocol.V2 {
		problem = p.scalarProblem(p.planTable(), deadline)
	} else {
		problem = p.searchProblem(deadline)
	}
	result, err := scheduler.Select[capacityResult](p.settings.PlanSearchStrategy).Search(problem, e)
	finished := time.Now()
	result.Diagnostics.StartedAt, result.Diagnostics.FinishedAt = started.UTC(), finished.UTC()
	result.Diagnostics.Duration = finished.Sub(started)
	e.record(result.Diagnostics)
	return result.Value, err
}
