package serving

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestCapacityRowRecordsBatchEnergy(t *testing.T) {
	s, snap := capacityFixture()
	s.CapacityGridSteps = 20
	s.CapacityMinimumRequests = 200
	p := snap.Options[0].Instance.Profile
	p.Batches = []BatchProfile{
		schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 4, MemoryBytes: 100}, []float64{1e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 2, EnergyPerInputJoules: 1, MemoryBytes: 100}, []float64{1e6}),
	}
	snap.Options[0].Instance.MaxBatch = 2
	snap.Workloads[0].SLO.TargetLatency = 100 * time.Millisecond
	row := capacityBuildForTest(t, s, snap)
	if len(row.Grid) != s.CapacityGridSteps {
		t.Fatalf("missing energy grid: %+v", row)
	}
	low := row.Grid[0].EnergyPerInputJoules
	high := row.Grid[len(row.Grid)/2].EnergyPerInputJoules
	if low <= 1 || low > 4 || high < 1 || high >= low {
		t.Fatalf("batching must move energy toward b2: low %g, high %g, grid %+v", low, high, row.Grid)
	}
}

func TestCapacityJudgmentEnergyUsesCompleteWindows(t *testing.T) {
	s, _ := capacityFixture()
	s.CapacityWindows, s.CapacityPassingWindows = 3, 2
	b := capacityBuilder{settings: s, percentile: 95, replay: func(_ context.Context, _ float64, n, seed int) (capacityWindowResult, error) {
		if seed == 1 {
			return capacityWindowResult{latency: -1}, nil
		}
		return capacityWindowResult{latency: 1, requestJoules: float64(seed * n), requests: n, met: true}, nil
	}}
	j, err := b.judge(context.Background(), 1)
	if err != nil || j.EnergyPerInputJoules != 2.5 || len(j.PercentileMS) != 3 {
		t.Fatalf("exclude incomplete window from energy and population: %+v, %v", j, err)
	}
	b.replay = func(context.Context, float64, int, int) (capacityWindowResult, error) {
		return capacityWindowResult{latency: -1}, nil
	}
	j, err = b.judge(context.Background(), 1)
	if err != nil || j.EnergyPerInputJoules != 0 {
		t.Fatalf("no complete windows must leave zero energy: %+v, %v", j, err)
	}
}

func TestCapacityRequestEnergyClipsWithoutComparedNodes(t *testing.T) {
	b := &BatchProfile{EnergyPerInputJoules: 4}
	i := &schedulerReplayInstance{batch: make([]*schedulerReplayRequest, 2)}
	s := schedulerReplayState{now: 5, horizon: 10, compared: map[NodeReference]bool{}}
	s.addBatchEnergy(i, b, 20)
	if math.Abs(s.out.requestJoules-2) > 1e-12 || s.out.energy != 0 {
		t.Fatalf("clipped request joules must not change compared energy: %+v", s.out)
	}
}
