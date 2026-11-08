package v2

import (
	"math"
	"slices"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

// capacityPrices owns Tick-local buffers; the fixed iteration loop allocates nothing.
type capacityPrices struct {
	planner                           protocol.Problem
	Table                             *protocol.Table
	Choice                            []int
	mu, rho, Demand, Supply, retained []float64
	Route                             float64
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

// PricedStart returns table indices, including every searchable cap variant.
// Its dual value is diagnostic only: the linearized energy is not a lower bound
// on the exact objective, whose request-energy shares depend on the selection.
func PricedStart(p protocol.Problem, t *protocol.Table) ([]int, float64) {
	Group(t)
	costs, kappas := []float64{}, []float64{}
	for _, n := range t.NodeOrder {
		if !p.Table.Compared[n] {
			continue
		}
		for _, e := range t.EntryOrder[n] {
			for _, q := range t.Entries[e].Variants {
				if !t.Searchable[q] || t.Keep[q] {
					continue
				}
				costs = append(costs, t.Energy(q, 0, p.Table.Period))
				if w := t.Entries[e].Workload; w >= 0 && t.Kappa[q][w] != 0 {
					kappas = append(kappas, t.Kappa[q][w])
				}
			}
		}
	}
	cost, kappa := pricedMedian(costs), pricedMedian(kappas)
	if cost == 0 || kappa == 0 {
		return nil, 0
	}
	c := capacityPrices{planner: p, Table: t, Choice: make([]int, len(p.Plans)),
		mu: make([]float64, len(p.Workloads)), rho: make([]float64, len(p.Workloads)), Demand: make([]float64, len(p.Workloads)),
		Supply: make([]float64, len(p.Workloads)), retained: make([]float64, len(p.Workloads))}
	for w := range p.Workloads {
		c.rho[w] = 1
		for _, n := range t.NodeOrder {
			c.retained[w] += t.KappaRet[t.ByPlan[n][0]][w]
		}
	}
	alpha := cost / kappa
	for iteration := 1; iteration <= 100; iteration++ {
		c.selectPlans()
		step := alpha / math.Sqrt(float64(iteration))
		c.aggregate()
		for w := range p.Workloads {
			c.mu[w] = max(0, c.mu[w]+step*(c.Demand[w]-c.Supply[w]))
		}
	}
	// Select x(mu_T) with the previous selection's route, retained supply and rho.
	dual := c.selectPlans()
	for w := range p.Workloads {
		dual += c.mu[w] * c.Demand[w]
	}
	return c.Choice, dual
}

func (c *capacityPrices) planCost(q int) float64 {
	t, p := c.Table, c.planner
	cost := t.Energy(q, c.Route, p.Table.Period)
	for w := range p.Workloads {
		cost += (p.Table.Period-c.Route)*c.rho[w]*t.KappaE[q][w] - c.mu[w]*t.Kappa[q][w]
	}
	return cost
}

func (c *capacityPrices) selectPlans() float64 {
	p, t := c.planner, c.Table
	for w := range p.Workloads {
		c.Demand[w], _ = CoverDemand(p.Workloads[w], c.retained[w], c.Route)
	}
	dual := 0.0
	for _, n := range t.NodeOrder {
		best, cost := t.ByPlan[n][0], math.Inf(1)
		if p.Table.Compared[n] {
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
		} else {
			cost = c.planCost(best)
		}
		c.Choice[n] = best
		dual += cost
	}
	return dual
}

func (c *capacityPrices) aggregate() {
	c.Route = 0
	clear(c.Supply)
	clear(c.retained)
	for _, n := range c.Table.NodeOrder {
		q := c.Choice[n]
		if !c.Table.Keep[q] {
			c.Route = max(c.Route, c.Table.TPre[q])
		}
		for w := range c.planner.Workloads {
			c.Supply[w] += c.Table.Kappa[q][w]
			c.retained[w] += c.Table.KappaRet[q][w]
		}
	}
	for w := range c.planner.Workloads {
		c.rho[w] = 1
		if c.Supply[w] != 0 {
			c.rho[w] = min(1, c.planner.Workloads[w].Rate/c.Supply[w])
		}
	}
}
