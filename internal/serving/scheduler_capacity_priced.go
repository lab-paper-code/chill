package serving

import (
	"math"
	"slices"
)

// capacityPrices owns Tick-local buffers; the fixed iteration loop allocates nothing.
type capacityPrices struct {
	planner                           *capacityPlanner
	table                             *planTable
	choice                            []int
	mu, rho, demand, supply, retained []float64
	route                             float64
}

func capacityPricedMedian(values []float64) float64 {
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

// pricedStart returns table indices, including every searchable cap variant.
// Its dual value is diagnostic only: the linearized energy is not a lower bound
// on the exact objective, whose request-energy shares depend on the selection.
func (p *capacityPlanner) pricedStart(t *planTable) ([]int, float64) {
	costs, kappas := []float64{}, []float64{}
	for _, n := range t.nodeOrder {
		if !p.compared[n] {
			continue
		}
		for _, e := range t.entryOrder[n] {
			for _, q := range t.entries[e].variants {
				if !t.searchable[q] || t.keep[q] {
					continue
				}
				costs = append(costs, t.energy(q, 0, p.period))
				if w := t.entries[e].workload; w >= 0 && t.kappa[q][w] != 0 {
					kappas = append(kappas, t.kappa[q][w])
				}
			}
		}
	}
	cost, kappa := capacityPricedMedian(costs), capacityPricedMedian(kappas)
	if cost == 0 || kappa == 0 {
		return nil, 0
	}
	c := capacityPrices{planner: p, table: t, choice: make([]int, len(p.plans)),
		mu: make([]float64, len(p.ws)), rho: make([]float64, len(p.ws)), demand: make([]float64, len(p.ws)),
		supply: make([]float64, len(p.ws)), retained: make([]float64, len(p.ws))}
	for w := range p.ws {
		c.rho[w] = 1
		for _, n := range t.nodeOrder {
			c.retained[w] += t.kappaRet[t.byPlan[n][0]][w]
		}
	}
	alpha := cost / kappa
	for iteration := 1; iteration <= 100; iteration++ {
		c.selectPlans()
		step := alpha / math.Sqrt(float64(iteration))
		c.aggregate()
		for w := range p.ws {
			c.mu[w] = max(0, c.mu[w]+step*(c.demand[w]-c.supply[w]))
		}
	}
	// Select x(mu_T) with the previous selection's route, retained supply and rho.
	dual := c.selectPlans()
	for w := range p.ws {
		dual += c.mu[w] * c.demand[w]
	}
	return c.choice, dual
}

func (c *capacityPrices) planCost(q int) float64 {
	t, p := c.table, c.planner
	cost := t.energy(q, c.route, p.period)
	for w := range p.ws {
		cost += (p.period-c.route)*c.rho[w]*t.kappaE[q][w] - c.mu[w]*t.kappa[q][w]
	}
	return cost
}

func (c *capacityPrices) selectPlans() float64 {
	p, t := c.planner, c.table
	for w := range p.ws {
		c.demand[w], _ = capacityCoverDemand(p.ws[w], c.retained[w], c.route)
	}
	dual := 0.0
	for _, n := range t.nodeOrder {
		best, cost := t.byPlan[n][0], math.Inf(1)
		if p.compared[n] {
			for _, e := range t.entryOrder[n] {
				for _, q := range t.entries[e].variants {
					if !t.searchable[q] {
						continue
					}
					value := c.planCost(q)
					if value < cost || value == cost && t.entryOf[q] == t.entryOf[best] && q < best {
						best, cost = q, value
					}
				}
			}
		} else {
			cost = c.planCost(best)
		}
		c.choice[n] = best
		dual += cost
	}
	return dual
}

func (c *capacityPrices) aggregate() {
	c.route = 0
	clear(c.supply)
	clear(c.retained)
	for _, n := range c.table.nodeOrder {
		q := c.choice[n]
		if !c.table.keep[q] {
			c.route = max(c.route, c.table.tPre[q])
		}
		for w := range c.planner.ws {
			c.supply[w] += c.table.kappa[q][w]
			c.retained[w] += c.table.kappaRet[q][w]
		}
	}
	for w := range c.planner.ws {
		c.rho[w] = 1
		if c.supply[w] != 0 {
			c.rho[w] = min(1, c.planner.ws[w].rate/c.supply[w])
		}
	}
}
