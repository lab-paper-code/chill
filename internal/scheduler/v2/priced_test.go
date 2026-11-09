package v2

import (
	"math"
	"testing"

	"github.com/lab-paper-code/chill/internal/scheduler/protocol"
)

func TestHeuristicPricesUseCurrentScalarResidual(t *testing.T) {
	for _, trial := range []struct {
		rate, service, total, want float64
	}{
		{50, 20e6, 20e6, 15},
		{25, 40e6, 20e6, 22.5},
		{5, 50e6, 20e6, 68.5},
		{50, 20e6, 120e6, 65},
	} {
		tab := &protocol.Table{ByPlan: [][]int{{0}}, NodeOrder: []int{0}, Compared: []bool{false}, Keep: []bool{false},
			RoutingRate: [][]float64{{trial.rate}}, RetainedRoutingRate: [][]float64{{10}},
			RoutingRateServiceNanos: [][]float64{{trial.rate * trial.service}}, TPre: []float64{0.05},
			Period: 1, Energy: func(int, float64, float64) float64 { return 12 }}
		p := protocol.Problem{Table: tab, Plans: [][]protocol.Plan{{{Eligible: true}}},
			Workloads: []protocol.Workload{{Rate: 10, Queued: 2, TargetNanos: 100e6, TotalNanos: trial.total}}}
		c := heuristicPrices{planner: p, Table: tab, Choice: []int{0}, price: []float64{2},
			coefficient: make([]float64, 1), Demand: make([]float64, 1), Supply: make([]float64, 1),
			retained: make([]float64, 1), service: make([]float64, 1)}
		c.aggregate()
		if got := c.Demand[0]-c.Supply[0]; math.Abs(got-trial.want) > 1e-10 {
			t.Fatalf("trial=%+v scalar residual=%g want=%g", trial, got, trial.want)
		}
		if allocations := testing.AllocsPerRun(100, func() {
			c.selectPlans()
			c.aggregate()
		}); allocations != 0 {
			t.Fatalf("price iteration allocated %g times", allocations)
		}
	}
}
