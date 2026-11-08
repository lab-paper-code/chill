// Package v1 implements keep/empty K2/K3 neighborhood search.
package v1

import (
 "slices"
 "sync"
 "sync/atomic"
 "time"

 "github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

type Engine[R any] struct{}

func (Engine[R]) Search(p protocol.Problem, exact protocol.Exact[R]) (R, protocol.Diagnostics, error) {
 start := time.Now()
 d := protocol.Diagnostics{Strategy: protocol.V1, StartedAt: start.UTC()}
 keep, empty := make([]int, len(p.Plans)), make([]int, len(p.Plans))
 for n, plans := range p.Plans {
  empty[n] = max(0, slices.IndexFunc(plans, func(plan protocol.Plan) bool { return plan.Eligible && plan.Empty }))
 }
 starts := [][]int{keep}
 if !slices.Equal(empty, keep) { starts = append(starts, empty) }
 ends := make([]R, len(starts))
 names := [2]string{"keep", "empty"}
 for s, choice := range starts {
  started := time.Now()
  ends[s] = exact.Evaluate(slices.Clone(choice))
  if s == 0 { exact.SeedKeep(ends[s]) }
  d.Steps = append(d.Steps, protocol.Step{Start: names[s], Point: exact.Point(ends[s]), Duration: time.Since(started)})
 }
 for k := 2; k <= 3 && !d.BudgetCut; k++ {
  d.Neighborhood = k
  for s := range starts {
   for !d.BudgetCut {
    evaluated, started := d.Evaluated, time.Now()
    step, err := sweep(p, exact, exact.Choice(ends[s]), k, &d)
    if err != nil { var zero R; return zero, d, err }
    if step == nil || !exact.Better(*step, &ends[s]) {
     d.TrailingEvaluated += d.Evaluated-evaluated
     break
    }
    record := protocol.Step{Start: names[s], K: k, Point: exact.Point(*step), ChangedNodes: make([]int, 0, k), Evaluated: d.Evaluated-evaluated, Duration: time.Since(started)}
    for n, j := range exact.Choice(*step) {
     if j != exact.Choice(ends[s])[n] { record.ChangedNodes = append(record.ChangedNodes, n) }
    }
    d.Steps = append(d.Steps, record)
    ends[s] = *step
   }
  }
 }
 best := ends[0]
 for _, end := range ends[1:] {
  if exact.Bounded(exact.Choice(end)) && exact.Admits(end) && exact.Better(end, &best) { best = end }
 }
 d.TerminationReason = protocol.NoImprovement
 if d.BudgetCut { d.TerminationReason = protocol.BudgetCut; d.Steps[len(d.Steps)-1].BudgetCut = true }
 finished := time.Now()
 d.FinishedAt, d.Duration = finished.UTC(), finished.Sub(start)
 point := exact.Point(best)
 d.Key, d.Joules = point.Key, point.Joules
 return best, d, nil
}

// Each worker owns its result and observer slot. Exact ordering makes reduction deterministic.
func sweep[R any](p protocol.Problem, exact protocol.Exact[R], choice []int, k int, d *protocol.Diagnostics) (*R, error) {
 type move struct { n, j int }
 moves := []move{}
 for n, plans := range p.Plans {
  for j, plan := range plans { if j != choice[n] && plan.Eligible { moves = append(moves, move{n, j}) } }
 }
 bests, counts := make([]*R, p.Workers), make([]int, p.Workers)
 var next atomic.Int64
 var cut atomic.Bool
 var wg sync.WaitGroup
 for w := range p.Workers {
  wg.Add(1)
  go func() {
   defer wg.Done()
   c := slices.Clone(choice)
   var walk func(int, int)
   walk = func(i, left int) {
    m := moves[i]
    c[m.n] = m.j
    defer func() { c[m.n] = choice[m.n] }()
    if cut.Load() || time.Now().After(p.Deadline) { cut.Store(true); return }
    if exact.Bounded(c) {
     counts[w]++
     res := exact.Evaluate(slices.Clone(c))
     exact.Observe(res, w)
     if exact.Admits(res) && exact.Better(res, bests[w]) { bests[w] = &res }
    }
    for i2 := i+1; left > 1 && i2 < len(moves); i2++ { if moves[i2].n != m.n { walk(i2, left-1) } }
   }
   for i := int(next.Add(1))-1; i < len(moves) && p.Context.Err() == nil && !cut.Load(); i = int(next.Add(1))-1 { walk(i, k) }
  }()
 }
 wg.Wait()
 if err := p.Context.Err(); err != nil { return nil, err }
 var best *R
 for w, res := range bests { d.Evaluated += counts[w]; if res != nil && exact.Better(*res, best) { best = res } }
 d.BudgetCut = cut.Load()
 return best, nil
}
