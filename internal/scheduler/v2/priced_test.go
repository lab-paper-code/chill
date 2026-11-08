package v2

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func TestCapacityPricesFixedMultiWorkloadKeepCostAndAllocations(t *testing.T) {
	tab := &protocol.Table{
		ByPlan: [][]int{{0}}, NodeOrder: []int{0}, Compared: []bool{false}, Keep: []bool{true},
		Kappa: [][]float64{{7, 11, 0}}, KappaE: [][]float64{{2, 4, 0}}, KappaRet: [][]float64{{7, 11, 0}},
		Period: 60, Energy: func(int, float64, float64) float64 { return 12 },
	}
	problem := protocol.Problem{Table: tab, Plans: [][]protocol.Plan{{{Eligible: true}}}, Workloads: []protocol.Workload{
		{Rate: 3, Slack: 1}, {Rate: 5, Slack: 1}, {Rate: 1, Slack: 1},
	}}
	c := capacityPrices{
		planner: problem, Table: tab, Choice: make([]int, 1),
		mu: []float64{2, 3, 5}, rho: []float64{0.25, 0.5, 0.75},
		Demand: make([]float64, 3), Supply: make([]float64, 3), retained: make([]float64, 3), Route: 7,
	}
	want := 12.0 + (60-7)*0.25*2 - 2*7 + (60-7)*0.5*4 - 3*11
	if got := c.planCost(0); got != want {
		t.Fatalf("multi-Workload keep cost=%.17g want=%.17g", got, want)
	}
	if allocations := testing.AllocsPerRun(100, func() {
		c.selectPlans()
		c.aggregate()
	}); allocations != 0 {
		t.Fatalf("price iteration allocated %g times", allocations)
	}
	if c.Choice[0] != 0 || c.Supply[0] != 7 || c.Supply[1] != 11 {
		t.Fatalf("fixed multi-Workload keep lost: choice=%v supply=%v", c.Choice, c.Supply)
	}
}
