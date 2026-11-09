package v2

import (
	"math"
	"slices"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// heuristicPrices owns Tick-local buffers; the fixed iteration loop allocates nothing.
// Prices linearize the scalar h<=Target constraint at the previous selection.
// Neither this start nor its energy proxy certifies exact demand feasibility.
type heuristicPrices struct {
	planner                                               protocol.Problem
	Table                                                 *protocol.Table
	Choice                                                []int
	price, coefficient, Demand, Supply, retained, service []float64
	Route                                                 float64
}

func pricedMedian(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	middle := len(values) / 2
	if len(values)%2 != 0 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}

// PricedStart keeps the optional fixed-iteration start and every searchable cap.
// The second return is zero: this nonlinear heuristic has no certified dual bound.
func PricedStart(p protocol.Problem, t *protocol.Table) ([]int, float64) {
	Group(t)
	costs, rates := []float64{}, []float64{}
	for _, n := range t.NodeOrder {
		if !t.Compared[n] {
			continue
		}
		for _, e := range t.EntryOrder[n] {
			for _, q := range t.Entries[e].Variants {
				if !t.Searchable[q] || t.Keep[q] {
					continue
				}
				costs = append(costs, math.Abs(t.Energy(q, 0, t.Period)))
				for _, rate := range t.RoutingRate[q] {
					if rate > 0 {
						rates = append(rates, rate)
					}
				}
			}
		}
	}
	cost, rate := pricedMedian(costs), pricedMedian(rates)
	if cost == 0 || rate == 0 {
		return nil, 0
	}
	c := heuristicPrices{planner: p, Table: t, Choice: make([]int, len(p.Plans)),
		price: make([]float64, len(p.Workloads)), coefficient: make([]float64, len(p.Workloads)),
		Demand: make([]float64, len(p.Workloads)), Supply: make([]float64, len(p.Workloads)),
		retained: make([]float64, len(p.Workloads)), service: make([]float64, len(p.Workloads))}
	for n := range c.Choice {
		c.Choice[n] = t.ByPlan[n][0]
	}
	c.aggregate()
	alpha := cost / rate
	for iteration := 1; iteration <= 100; iteration++ {
		c.selectPlans()
		c.aggregate()
		step := alpha / math.Sqrt(float64(iteration))
		for w := range p.Workloads {
			c.price[w] = max(0, c.price[w]+step*(c.Demand[w]-c.Supply[w]))
		}
	}
	c.selectPlans()
	return c.Choice, 0
}

func (c *heuristicPrices) planCost(q int) float64 {
	t := c.Table
	cost := t.Energy(q, c.Route, t.Period)
	for w, workload := range c.planner.Workloads {
		if workload.TargetNanos > 0 && (workload.Rate > 0 || workload.Queued > 0) {
			cost += c.price[w] * (t.RoutingRateServiceNanos[q][w]/workload.TargetNanos - c.coefficient[w]*t.RoutingRate[q][w])
		}
	}
	return cost
}

func (c *heuristicPrices) selectPlans() {
	t := c.Table
	for _, n := range t.NodeOrder {
		best, cost := t.ByPlan[n][0], math.Inf(1)
		if t.Compared[n] {
			for _, e := range t.EntryOrder[n] {
				for _, q := range t.Entries[e].Variants {
					if !t.Searchable[q] {
						continue
					}
					value := c.planCost(q)
					if value < cost || value == cost && t.EntryOf[q] == t.EntryOf[best] && q < best {
						best, cost = q, value
					}
				}
			}
		}
		c.Choice[n] = best
	}
}

func (c *heuristicPrices) aggregate() {
	c.Route = 0
	clear(c.Supply)
	clear(c.retained)
	clear(c.service)
	for _, n := range c.Table.NodeOrder {
		q := c.Choice[n]
		if !c.Table.Keep[q] {
			c.Route = max(c.Route, c.Table.TPre[q])
		}
		for w := range c.planner.Workloads {
			c.Supply[w] += c.Table.RoutingRate[q][w]
			c.retained[w] += c.Table.RetainedRoutingRate[q][w]
			c.service[w] += c.Table.RoutingRateServiceNanos[q][w]
		}
	}
	for w, workload := range c.planner.Workloads {
		c.coefficient[w], c.Demand[w] = 0, 0
		if workload.TargetNanos <= 0 || workload.Rate == 0 && workload.Queued == 0 {
			c.Supply[w] = 0
			continue
		}
		backlog := max(0, float64(workload.Queued)+(workload.Rate-c.retained[w])*c.Route)
		c.coefficient[w] = (workload.TargetNanos - workload.TotalNanos - 1e9*c.Route) / workload.TargetNanos
		c.Demand[w] = 1e9 * backlog / workload.TargetNanos
		if c.Supply[w] < workload.Rate {
			horizon := max(c.Table.Period-c.Route, 0)
			c.coefficient[w] += 1e9 * horizon / workload.TargetNanos
			c.Demand[w] += 1e9 * workload.Rate * horizon / workload.TargetNanos
		}
		c.Supply[w] = c.coefficient[w]*c.Supply[w] - c.service[w]/workload.TargetNanos
	}
}
