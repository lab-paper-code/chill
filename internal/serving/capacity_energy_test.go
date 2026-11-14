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
		schedulerTestBatchProfile(BatchProfile{Batch: 1, InputSlopeJoules: schedulerTestSlope(4), MemoryBytes: 100}, []float64{1e6}),
		schedulerTestBatchProfile(BatchProfile{Batch: 2, InputSlopeJoules: schedulerTestSlope(1), MemoryBytes: 100}, []float64{1e6}),
	}
	snap.Options[0].Instance.MaxBatch = 2
	snap.Workloads[0].SLO.TargetLatency = 100 * time.Millisecond
	row := capacityBuildForTest(t, s, snap)
	if len(row.Grid) != s.CapacityGridSteps {
		t.Fatalf("missing energy grid: %+v", row)
	}
	// Node power charges every input the slope of the smallest batch whose
	// saturation rate (b1: 1000/s, b2: 2000/s) covers the instance's rate, so
	// a rate b1 covers costs b1's 4 J and a rate only b2 covers costs b2's 1 J,
	// less the inputs the horizon clips.
	low := row.Grid[0].EnergyPerInputJoules
	high := row.Grid[len(row.Grid)-1].EnergyPerInputJoules
	if low <= 3.9 || low > 4 || high <= 0.9 || high > 1 {
		t.Fatalf("batching must move energy toward b2: low %g, high %g, grid %+v", low, high, row.Grid)
	}
}

func TestCapacityJudgmentEnergyUsesCompleteWindows(t *testing.T) {
	s, _ := capacityFixture()
	s.CapacityWindows, s.CapacityPassingWindows = 3, 2
	b := capacityBuilder{settings: s, percentile: 95, replay: func(_ context.Context, _ float64, n, seed int) (capacityWindowResult, error) {
		if seed == 1 {
			return capacityWindowResult{latencyMS: -1}, nil
		}
		return capacityWindowResult{latencyMS: 1, requestJoules: float64(seed * n), requests: n, met: true}, nil
	}}
	j, err := b.judge(context.Background(), 1)
	if err != nil || j.EnergyPerInputJoules != 2.5 || len(j.PercentileMS) != 3 {
		t.Fatalf("exclude incomplete window from energy and population: %+v, %v", j, err)
	}
	b.replay = func(context.Context, float64, int, int) (capacityWindowResult, error) {
		return capacityWindowResult{latencyMS: -1}, nil
	}
	j, err = b.judge(context.Background(), 1)
	if err != nil || j.EnergyPerInputJoules != 0 {
		t.Fatalf("no complete windows must leave zero energy: %+v, %v", j, err)
	}
}

// A started batch's inputs are clipped to the horizon (2 inputs over 5 of 20
// ns is 0.5 input); with no compared node the load joules they are charged
// (slope 4 J, no awake step: 2 J) count as request joules only.
func TestCapacityRequestEnergyClipsWithoutComparedNodes(t *testing.T) {
	_, snap := schedulerFixture()
	config := snap.Instances[0]
	profile := *config.Profile
	profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1, InputSlopeJoules: schedulerTestSlope(4), MemoryBytes: 100}, []float64{1})}
	config.Profile = &profile
	i := &schedulerReplayInstance{config: config, batch: make([]*schedulerReplayRequest, 2)}
	s := schedulerReplayState{now: 5, horizon: 10, compared: map[NodeReference]bool{}, instances: []*schedulerReplayInstance{i}}
	s.addBatchEnergy(i, nil, 20)
	s.addLoadEnergy()
	if math.Abs(s.out.requestJoules-2) > 1e-12 || s.out.energy != 0 {
		t.Fatalf("clipped request joules must not change compared energy: %+v", s.out)
	}
}
