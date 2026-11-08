package v2

import (
 "github.com/lab-paper-code/chill/internal/scheduler/protocol"
	"math"
	"time"
)

// CoverDemand returns λ̃ and B>0, including the slack<=0 boundary.
func CoverDemand(w protocol.Workload, retained, route float64) (float64, bool) {
	backlog := float64(w.Queued) + max(0, w.Rate-retained)*route
	if backlog == 0 {
		return w.Rate, false
	}
	if w.Slack <= 0 {
		return math.Inf(1), true
	}
	return max(w.Rate, backlog/w.Slack), true
}

func coverageDelta(before, after protocol.Rank) float64 {
	change := 0.0
	for w := range before.Demand {
		change += min(after.Demand[w], after.Supply[w]) - min(before.Demand[w], before.Supply[w])
	}
	return change
}

func (c *v2Search) movePlans(n int, overload bool, dst []int) []int {
 s,t := c.s,c.s.Table
 dst=dst[:0]
 for _,e := range t.EntryOrder[n] {
  if e==t.EntryOf[s.Choice[n]] { continue }
  entry:=t.Entries[e]
  state:=CapState{Overload:overload}
  if w:=entry.Workload;w>=0 {
   q:=entry.Variants[0]
   r:=0.0
   for node,pre:=range s.port.RouteTimes() { if node==n { pre=0; if !t.Keep[q] { pre=t.TPre[q] } };r=max(r,pre) }
   demand,retained,kappa,kappaE:=s.port.MoveTerms(w,n,q)
   state.Demand,state.Backlog=CoverDemand(demand,retained,r)
   state.KappaOthers,state.KappaEOthers=kappa,kappaE
  }
  caps:=SelectCaps(t,entry,state)
  if caps.Count==2 && t.KeyParts[caps.Candidates[1]]<t.KeyParts[caps.Candidates[0]] { caps.Candidates[0],caps.Candidates[1]=caps.Candidates[1],caps.Candidates[0] }
  for _,q:=range caps.Candidates[:caps.Count] { if t.Searchable[q] { dst=append(dst,q) } }
 }
 return dst
}

func (c *v2Search) cover() {
	for (!c.current.Feasible || !c.current.Bounded) && !c.expired() {
		evaluated, stepStart := c.stats.SurrogateEvaluated, time.Now()
		found, bestEnergy, bestCoverage := false, 0.0, 0.0
		c.current.CopyInto(&c.best)
		for _, n := range c.s.Table.NodeOrder {
			c.moves[n] = c.movePlans(n, !c.current.Feasible, c.moves[n])
			for _, q := range c.moves[n] {
				key, ok := c.evaluate(n, q)
				if !ok {
					break
				}
				coverage, energy := coverageDelta(c.current, key), key.Joules-c.current.Joules
				if !c.coverAllows(key, coverage) {
					continue
				}
				better := !found
				if found {
					if c.current.CountExcess > 0 {
						better = coverage > bestCoverage
					} else if energy <= 0 || bestEnergy <= 0 {
						better = energy < bestEnergy || energy == bestEnergy && coverage > bestCoverage
					} else {
						better = energy/coverage < bestEnergy/bestCoverage
					}
					if coverage == bestCoverage && (c.current.CountExcess > 0 || energy == bestEnergy) {
						better = c.moveLess(n, q, c.best)
					} else if c.current.CountExcess == 0 && energy > 0 && bestEnergy > 0 && energy/coverage == bestEnergy/bestCoverage {
						better = c.moveLess(n, q, c.best)
					}
				}
				if better {
					key.CopyInto(&c.best)
					found, bestEnergy, bestCoverage = true, energy, coverage
				}
			}
			if c.expired() {
				break
			}
		}
		if !found {
			return
		}
		c.recordStep(c.best, c.current.Choice, protocol.Cover, c.stats.SurrogateEvaluated-evaluated, stepStart)
		c.s.reset(c.best.Choice)
		c.best.CopyInto(&c.current)
		c.finalists.add(c.current, c.Start)
	}
}

func (c *v2Search) coverAllows(key protocol.Rank, coverage float64) bool {
	allowed := key.Searchable && !key.UnroutedPrepare
	if c.current.CountExcess > 0 {
		allowed = allowed && key.Instances < c.current.Instances && key.CountExcess < c.current.CountExcess
	} else {
		allowed = allowed && key.Bounded && coverage > 0
		for w, satisfied := range c.current.Satisfied {
			allowed = allowed && (!satisfied || key.Satisfied[w])
		}
	}
	return allowed
}

// UID order wins a cover tie before the changed plan's logical key.
func (c *v2Search) moveLess(n, q int, best protocol.Rank) bool {
	for _, node := range c.s.Table.NodeOrder {
		if best.Choice[node] != c.current.Choice[node] {
			if node != n {
				return c.s.Table.NodeUID[n] < c.s.Table.NodeUID[node]
			}
			return c.s.Table.KeyParts[q] < c.s.Table.KeyParts[best.Choice[node]]
		}
	}
	return false
}
