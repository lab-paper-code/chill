package serving

import (
	"context"
	"math"
	"math/rand/v2"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func capacityFixture() (SchedulerSettings, SchedulerSnapshot) {
	s, snap := schedulerFixture()
	s.CapacityWindow, s.CapacityWindows, s.CapacityPassingWindows, s.CapacityMinimumRequests = time.Second, 3, 2, 200
	s.CompletionMarginMS = 0
	c := snap.Instances[0].Profile.Condition
	snap.DispatchOccupancy = []DispatchOccupancy{{Node: snap.Nodes[0].Node, PowerMode: c.PowerMode, RuntimeImage: c.RuntimeImage, Model: c.LogicalModel, Source: "fixture"}}
	p := *snap.Instances[0].Profile
	p.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 1, EnergyPerInputJoules: 0.01, MemoryBytes: 100}, []float64{8e5, 1e6, 1.2e6})}
	snap.Instances[0].Profile = &p
	snap.Options[0].Instance = snap.Instances[0]
	snap.Workloads[0].SLO = SLO{Percentile: 95, TargetLatency: 20 * time.Millisecond}
	return s, snap
}

func capacityBuildForTest(t *testing.T, s SchedulerSettings, snap SchedulerSnapshot) CapacityRow {
	t.Helper()
	row, err := BuildCapacityRow(context.Background(), s, snap.DispatchOccupancy, snap.Options[0], snap.Workloads[0], snap.At, ResolvePathBudget(s, snap.PathBudgets, snap.Workloads[0]))
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// Seeded arrivals and conditional service draws must make row builds repeatable.
func TestCapacityRowKappaIsRepeatable(t *testing.T) {
	s, snap := capacityFixture()
	row := capacityBuildForTest(t, s, snap)
	if row.Status != CapacityBuilt || row.Kappa == nil || *row.Kappa <= 0 || *row.Kappa >= row.Mu {
		t.Fatalf("fixture row has no interior capacity: %+v", row)
	}
	again := capacityBuildForTest(t, s, snap)
	if *again.Kappa != *row.Kappa || !reflect.DeepEqual(again.Grid, row.Grid) || !reflect.DeepEqual(again.Bisection, row.Bisection) || again.Key != row.Key {
		t.Fatal("the same row built twice differs")
	}
}

func TestCapacityTableRecoversCurrentPlacementWithoutPoisoningMeasuredRow(t *testing.T) {
	const gpuUUID = "gpu"
	for _, resource := range []string{"memory", "device"} {
		t.Run(resource, func(t *testing.T) {
			settings, snapshot := capacityFixture()
			if resource == "device" {
				snapshot.Instances[0].Allocation.GPUUUID = gpuUUID
				snapshot.Options[0].Instance.Allocation.GPUUUID = gpuUUID
				snapshot.Nodes[0].GPUUUIDs = []string{gpuUUID}
			}
			healthy := snapshot.Nodes[0]
			if resource == "memory" {
				snapshot.Nodes[0].MemoryBytes = snapshot.Instances[0].Allocation.MemoryBytes - 1
			} else {
				snapshot.Nodes[0].GPUUUIDs = nil
			}
			var recorded []CapacityRow
			table := NewCapacityTable(1, func(row CapacityRow) error { recorded = append(recorded, row); return nil }, nil)
			table.Ensure(context.Background(), settings, snapshot)
			table.Wait()
			initial := table.Rows()[0]
			blocked, err := EvaluateScheduler(context.Background(), settings, snapshot, table.Rows())
			if err != nil || len(blocked.Keep.Reasons) == 0 || blocked.Target != nil {
				t.Fatalf("current resource absence did not block execution: %+v %v", blocked, err)
			}
			snapshot.Nodes[0] = healthy
			table.Ensure(context.Background(), settings, snapshot)
			table.Wait()
			restored := table.Rows()[0]
			if restored.Status != CapacityBuilt || restored.Kappa == nil || *restored.Kappa <= 0 {
				t.Fatalf("recovered placement stuck at old rejection: initial=%+v restored=%+v", initial, restored)
			}
			recovered, err := EvaluateScheduler(context.Background(), settings, snapshot, table.Rows())
			if err != nil || len(recovered.Keep.Reasons) != 0 {
				t.Fatalf("recovered current resources remain rejected: %+v %v", recovered.Keep, err)
			}
			if initial.Status != CapacityBuilt || initial.Key != restored.Key || !initial.BuiltAt.Equal(restored.BuiltAt) ||
				len(recorded) != 1 || recorded[0].Status != CapacityBuilt {
				t.Fatalf("current inventory contaminated cached or archived capacity: %+v %+v records=%+v", initial, restored, recorded)
			}
			snapshot.Nodes = nil
			table.Ensure(context.Background(), settings, snapshot)
			table.Wait()
			if row := table.Rows()[0]; row.Key != restored.Key || row.Status != CapacityBuilt {
				t.Fatalf("missing current snapshot changed measured row: %+v", row)
			}
		})
	}
}

// A completion margin above the SLO target leaves no rate that meets it.
func TestCapacityMarginAboveTargetIsZero(t *testing.T) {
	s, snap := capacityFixture()
	s.CompletionMarginMS = 139
	snap.Workloads[0].SLO = SLO{Percentile: 99, TargetLatency: 100 * time.Millisecond}
	if row := capacityBuildForTest(t, s, snap); row.Kappa == nil || *row.Kappa != 0 || row.Reason != CapacityServiceExceedsSLO || len(row.Grid) != 0 {
		t.Fatalf("margin 139 ms, p99 100 ms: %+v", row)
	}
}

func TestCapacityServiceLowerBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples []float64
		smaller bool
		skip    bool
	}{
		{"all samples exceed", []float64{3.1e6, 4e6}, false, true},
		{"equality replays", []float64{3e6, 4e6}, false, false},
		{"mean is not a bound", []float64{1e6, 8e6}, false, false},
		{"smaller batch can fit", []float64{3.1e6, 4e6}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, snap := capacityFixture()
			s.CompletionMarginMS = 29
			s.CapacityWindow, s.CapacityWindows, s.CapacityPassingWindows = time.Millisecond, 1, 1
			snap.Workloads[0].SLO = SLO{Percentile: 95, TargetLatency: 32 * time.Millisecond}
			p := snap.Options[0].Instance.Profile
			p.Batches = []BatchProfile{
				schedulerTestBatchProfile(BatchProfile{Batch: 1, MemoryBytes: 100}, tc.samples),
				schedulerTestBatchProfile(BatchProfile{Batch: 2, MemoryBytes: 100}, tc.samples),
			}
			snap.Options[0].Instance.MaxBatch = 2
			if tc.smaller {
				p.Batches[0] = schedulerTestBatchProfile(p.Batches[0], []float64{1e6})
			}
			row := capacityBuildForTest(t, s, snap)
			if tc.skip {
				if row.Status != CapacityBuilt || row.Kappa == nil || *row.Kappa != 0 || row.Reason != CapacityServiceExceedsSLO || row.MinimumServiceNanos != 3.1e6 || len(row.Grid) != 0 || len(row.Bisection) != 0 || row.BuiltAt.IsZero() || row.Elapsed <= 0 {
					t.Fatalf("missing analytical zero and evidence: %+v", row)
				}
			} else if row.Reason == CapacityServiceExceedsSLO || len(row.Grid) != s.CapacityGridSteps {
				t.Fatalf("a potentially fitting batch must replay: %+v", row)
			}
		})
	}
}

type capacityCountingContext struct {
	context.Context
	calls int
}

func (c *capacityCountingContext) Err() error {
	c.calls++
	return c.Context.Err()
}

func TestCapacityWindowStopsOnlyAfterCertainFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		service    float64
		percentile float64
		failed     bool
	}{
		{"passing window is complete", 5e5, 95, false},
		{"overload p95", 5e6, 95, true},
		{"overload p100", 5e6, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const n = 400
			s, snap := schedulerReplayWindow(n)
			snap.Instances[0].Profile.Batches[0] = schedulerTestBatchProfile(snap.Instances[0].Profile.Batches[0], []float64{tc.service})
			d := snap.Workloads[0]
			d.SLO = SLO{Percentile: tc.percentile, TargetLatency: 20 * time.Millisecond}
			keep := SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}
			fullCtx := &capacityCountingContext{Context: context.Background()}
			full, err := schedulerReplayEstimate(fullCtx, s, snap, keep, 1, nil, nil)
			if err != nil || len(full.Reasons) != 0 {
				t.Fatalf("full replay failed: %v %+v", err, full)
			}
			limit := capacityWindowLimit{requests: n, slo: d.SLO, percentile: ResolvePathBudget(s, snap.PathBudgets, d).JudgmentPercentile}
			earlyCtx := &capacityCountingContext{Context: context.Background()}
			early, err := schedulerReplayEstimate(earlyCtx, s, snap, keep, 1, nil, &limit)
			if err != nil || limit.failed != tc.failed || (full.Status != schedulerStatusFeasible) != tc.failed {
				t.Fatalf("window verdict changed: err %v, failed %v, full %+v", err, limit.failed, full)
			}
			if tc.failed {
				if earlyCtx.calls >= fullCtx.calls || len(early.SLO) != 0 {
					t.Fatalf("failed window must skip replay calls, not publish partial percentiles: early %d, full %d, %+v", earlyCtx.calls, fullCtx.calls, early)
				}
			} else if !reflect.DeepEqual(early, full) {
				t.Fatalf("passing window changed: early %+v, full %+v", early, full)
			}
		})
	}
	// Empirical p95 of 20 permits exactly one miss; equality is not a miss,
	// and observing the same late request twice must not count it twice.
	limit := capacityWindowLimit{requests: 20, slo: SLO{Percentile: 95, TargetLatency: time.Millisecond}, percentile: 95}
	late := &schedulerReplayRequest{arrival: 0}
	s := schedulerReplayState{now: 2e6, capacity: &limit, live: []*schedulerReplayRequest{late, {arrival: 1e6}}}
	for range 2 {
		if s.capacityWindowFailed() || limit.missed != 1 {
			t.Fatalf("one late request must still allow p95: %+v", limit)
		}
	}
	s.live = append(s.live, &schedulerReplayRequest{arrival: 0})
	if !s.capacityWindowFailed() || limit.missed != 2 {
		t.Fatalf("second miss must make p95 impossible: %+v", limit)
	}
}

func TestCapacityRowWithoutOccupancyUsesConfiguredDefault(t *testing.T) {
	s, snap := capacityFixture()
	snap.DispatchOccupancy = nil
	s.DefaultDispatchOccupancyNanos = int64(time.Millisecond)
	row := capacityBuildForTest(t, s, snap)
	if row.Status != CapacityBuilt || row.Kappa == nil || *row.Kappa <= 0 {
		t.Fatalf("default d did not produce capacity: %+v", row)
	}
	d := row.Input.DispatchOccupancy
	if d == nil || d.PerRequest != time.Millisecond || d.Provenance.Source != "default" || d.Provenance.Value != int64(time.Millisecond) {
		t.Fatalf("missing default provenance: %+v", d)
	}
	s.DefaultDispatchOccupancyNanos = 0
	zero := capacityBuildForTest(t, s, snap)
	if zero.Kappa == nil || *zero.Kappa <= *row.Kappa || zero.Key == row.Key {
		t.Fatalf("zero scalar default not used exactly once per request: %+v", zero)
	}
}

func TestCapacityRowUsesQualifiedMeasuredCacheEntryBeforeDOM(t *testing.T) {
	s, snap := capacityFixture()
	s.DefaultDispatchOccupancyNanos = int64(2 * time.Millisecond)
	i := snap.Options[0].Instance
	snap.DispatchOccupancy[0].PerRequest = time.Millisecond
	window := OccupancyWindow{Start: snap.At.Add(-time.Minute), End: snap.At}
	// The incoming cache publishes only entries meeting its minimumPairs policy.
	entry := DispatchOccupancy{
		Node: i.Node, PowerMode: i.Profile.Condition.PowerMode, Model: i.Profile.Condition.LogicalModel,
		ProfileID: i.Profile.ID, ExecutionPath: i.Profile.Condition.Artifact.ExecutionPath, Batch: i.MaxBatch,
		PerRequest: 500 * time.Microsecond, Source: "rolling-cache",
		Provenance: &DispatchOccupancyProvenance{Source: "measured", Pairs: 1000, Window: &window, UpdatedAt: snap.At, Value: 500000},
	}
	snap.DispatchOccupancy = append(snap.DispatchOccupancy, entry)
	row := capacityBuildForTest(t, s, snap)
	d := row.Input.DispatchOccupancy
	if row.Status != CapacityBuilt || row.Kappa == nil || *row.Kappa <= 0 ||
		d.Source != entry.Source || !reflect.DeepEqual(d.Provenance, entry.Provenance) {
		t.Fatalf("qualified measured entry lost priority/provenance: %+v", row)
	}
}

func TestResolveDispatchOccupancyExactMeasuredAndLegacyDOM(t *testing.T) {
	s, snap := capacityFixture()
	s.DefaultDispatchOccupancyNanos = int64(2 * time.Millisecond)
	i := snap.Options[0].Instance
	dom := snap.DispatchOccupancy[0]
	dom.RuntimeImage, dom.PerRequest = "historical-image", time.Millisecond
	measured := dom
	measured.ProfileID, measured.ExecutionPath, measured.Batch = i.Profile.ID, i.Profile.Condition.Artifact.ExecutionPath, i.MaxBatch
	measured.PerRequest = 500 * time.Microsecond
	measured.Provenance = &DispatchOccupancyProvenance{Source: "measured", Pairs: 1000, Value: 500000}
	for _, tc := range []struct {
		name   string
		change func(*DispatchOccupancy)
	}{
		{"node", func(d *DispatchOccupancy) { d.Node.UID = schedulerTestOther }},
		{"model", func(d *DispatchOccupancy) { d.Model = schedulerTestOther }},
		{"mode", func(d *DispatchOccupancy) { d.PowerMode = schedulerTestOther }},
		{"profile", func(d *DispatchOccupancy) { d.ProfileID = schedulerTestOther }},
		{"path", func(d *DispatchOccupancy) { d.ExecutionPath = schedulerTestOther }},
		{"batch", func(d *DispatchOccupancy) { d.Batch++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mismatch := measured
			tc.change(&mismatch)
			got := ResolveDispatchOccupancy(s, []DispatchOccupancy{mismatch, dom}, i)
			if got.PerRequest != dom.PerRequest || got.Provenance.Source != "dom" || got.Source != dom.Source {
				t.Fatalf("nonmatching measured entry beat legacy DOM: %+v", got)
			}
			got = ResolveDispatchOccupancy(s, []DispatchOccupancy{mismatch}, i)
			if got.Provenance.Source != "default" || got.PerRequest != 2*time.Millisecond {
				t.Fatalf("nonmatching measured entry blocked configured fallback: %+v", got)
			}
		})
	}
	// DOM retains wildcard batch/profile matching and ignores image history.
	other := i
	profile := *i.Profile
	profile.ID, profile.Condition.Artifact.ExecutionPath = "another-profile", "another-path"
	other.Profile, other.MaxBatch = &profile, 8
	got := ResolveDispatchOccupancy(s, []DispatchOccupancy{dom}, other)
	if got.PerRequest != time.Millisecond || got.Provenance.Source != "dom" || got.ProfileID != profile.ID || got.Batch != 8 {
		t.Fatalf("legacy DOM wildcard changed: %+v", got)
	}
	// A recorded resolved default retains its source/value on readback, even
	// when a caller supplies unrelated current settings.
	s.DefaultDispatchOccupancyNanos = 0
	projected := ResolveDispatchOccupancy(s, nil, other)
	s.DefaultDispatchOccupancyNanos = 123
	if got := ResolveDispatchOccupancy(s, []DispatchOccupancy{*projected}, other); got.PerRequest != 0 ||
		got.Provenance.Source != occupancySourceDefault || got.Provenance.Value != 0 || got.Provenance == projected.Provenance {
		t.Fatalf("recorded default provenance/value lost or aliased: %+v", got)
	}
	// Clamp operational d only; never rewrite the incoming signed measurement.
	measured.PerRequest, measured.Provenance.Value = -100, -100
	got = ResolveDispatchOccupancy(s, []DispatchOccupancy{measured, dom}, i)
	if got.PerRequest != 0 || got.Provenance.Value != 0 || got.Provenance.Source != "measured" ||
		measured.PerRequest != -100 || measured.Provenance.Value != -100 {
		t.Fatalf("signed fact mutated or operational d not clamped: %+v %+v", measured, got)
	}
}

func TestCapacityConditionalSupportWithDispatchOccupancy(t *testing.T) {
	settings, snapshot := capacityFixture()
	settings.CapacityWindow, settings.CapacityWindows, settings.CapacityPassingWindows = time.Second, 1, 1
	settings.CapacityTailSamples = 10
	settings.DefaultDispatchOccupancyNanos = 0
	short, long := int64(0), int64(time.Second)
	batch := &snapshot.Instances[0].Profile.Batches[0]
	batch.ServiceSamples = []firstfloor.ServiceSample{
		{ServiceDurationNanos: int64(time.Millisecond), TrialID: "synthetic"},
		{ServiceDurationNanos: int64(time.Millisecond), PrecedingIdleNanos: &short, TrialID: "synthetic"},
		{ServiceDurationNanos: int64(100 * time.Millisecond), PrecedingIdleNanos: &long, TrialID: "synthetic"},
	}
	var err error
	batch.ServiceDistribution, err = firstfloor.NewServiceDistribution(batch.ServiceSamples)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Workloads[0].SLO = SLO{Percentile: 95, TargetLatency: 50 * time.Millisecond}
	without := capacityBuildForTest(t, settings, snapshot)
	// A marginal mean closes search below 30 req/s despite fast recorded
	// support becoming nearest once this actor stays busy.
	if without.Kappa == nil || *without.Kappa <= 30 {
		t.Fatalf("quiet-gap service incorrectly capped busy throughput: %+v", without)
	}
	snapshot.DispatchOccupancy[0].PerRequest = 4 * time.Millisecond
	with := capacityBuildForTest(t, settings, snapshot)
	if with.Kappa == nil || *with.Kappa >= *without.Kappa {
		t.Fatalf("conditional service discarded dispatch occupancy: without=%+v with=%+v", without, with)
	}
	if with.ConditionalService == nil || with.ConditionalService.Queries == 0 || with.ConditionalService.MinimumTrialCount != 1 {
		t.Fatalf("conditional support evidence missing: %+v", with)
	}
}

func TestCapacityWindowPopulation(t *testing.T) {
	s, _ := capacityFixture()
	s.CapacityWindow, s.CapacityMinimumRequests = 60*time.Second, 1000
	b := capacityBuilder{settings: s, percentile: 95}
	for _, c := range []struct {
		lambda float64
		want   int
	}{{1, 2000}, {50, 3000}, {145, 8700}} {
		if got := b.requests(c.lambda); got != c.want || float64(got) < c.lambda*60 {
			t.Fatalf("rate %g: %d requests, want %d", c.lambda, got, c.want)
		}
	}
}

// capacityStub meets the SLO below limit req/s, misses seeds in fail, and
// meets again from resume req/s on (0 disables).
func capacityStub(limit, resume float64, fail map[int]bool) func(context.Context, float64, int, int) (capacityWindowResult, error) {
	return func(_ context.Context, lambda float64, n, seed int) (capacityWindowResult, error) {
		return capacityWindowResult{requests: n, met: (lambda <= limit || resume > 0 && lambda >= resume) && !fail[seed]}, nil
	}
}

func TestCapacityGridBisectionAndMonotonicity(t *testing.T) {
	s, _ := capacityFixture()
	s.CapacityWindows, s.CapacityPassingWindows = 20, 19
	build := func(limit, resume float64, fail map[int]bool) CapacityRow {
		row := CapacityRow{Mu: 100}
		b := capacityBuilder{settings: s, percentile: 95, replay: capacityStub(limit, resume, fail)}
		if err := b.build(context.Background(), &row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	if row := build(42, 0, nil); row.Status != CapacityBuilt || *row.Kappa > 42 || *row.Kappa < 41.5 {
		t.Fatalf("kappa must bisect between 40 and 45 to 0.5 req/s: %+v", row)
	}
	if row := build(1, 0, nil); *row.Kappa != 0 {
		t.Fatalf("a missed first grid rate gives kappa 0: %v", *row.Kappa)
	}
	if row := build(1000, 0, nil); *row.Kappa != 100 {
		t.Fatalf("every grid rate met gives kappa mu: %v", *row.Kappa)
	}
	if row := build(30, 60, nil); row.Status != CapacityRejected || row.Reason != CapacityNonMonotone || row.Kappa != nil {
		t.Fatalf("met, missed, met grid must be NonMonotone without kappa: %+v", row)
	}
	// 19 of 20 windows meet; a second failing window misses and stops judging.
	if row := build(1000, 0, map[int]bool{7: true}); *row.Kappa != 100 {
		t.Fatalf("19/20 windows must meet: %+v", row.Grid[0])
	}
	row := build(1000, 0, map[int]bool{3: true, 4: true})
	if *row.Kappa != 0 || row.Grid[0].Passed != 2 || len(row.Grid[0].PercentileMS) != 4 {
		t.Fatalf("18/20 windows must miss after the second failure: %+v", row.Grid[0])
	}
}

func TestCapacityTableBuildsAutonomousRowsOnce(t *testing.T) {
	s, snap := capacityFixture()
	other := *snap.Workloads[0]
	other.UID = "w2"
	snap.Workloads = append(snap.Workloads, &other)
	snap.Options[0].CompatibleUIDs = []string{"w1", "w2"}
	var recorded, recordedW2 atomic.Int32
	table := NewCapacityTable(2, func(row CapacityRow) error {
		if row.Input.WorkloadUID == "w2" {
			recordedW2.Add(1)
		}
		recorded.Add(1)
		return nil
	}, nil)
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	rows := table.Rows()
	if len(rows) != 1 || rows[0].Input.WorkloadUID != "w1" || rows[0].Status != CapacityBuilt || recorded.Load() != 1 || recordedW2.Load() != 0 {
		t.Fatalf("only the autonomous Workload must build: %+v, records %d", rows, recorded.Load())
	}
	before := rows[0]
	// Inputs outside the row key start nothing.
	snap.At = snap.At.Add(time.Minute)
	s.ControlInterval, s.MaxReplicas = 2*s.ControlInterval, s.MaxReplicas+1
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	if row := table.Rows()[0]; row.Key != before.Key || row.BuiltAt != before.BuiltAt || recorded.Load() != 1 {
		t.Fatalf("unrelated change recomputed: %+v, records %d", row, recorded.Load())
	}
	// SLO, margin, profile and capacity settings each add one missing row.
	snap.Workloads[0].SLO.TargetLatency = 30 * time.Millisecond
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	s.CompletionMarginMS = 5
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	p := *snap.Options[0].Instance.Profile
	p.ID = "profile-2"
	snap.Options[0].Instance.Profile = &p
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	s.CapacityWindows = 4
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	if recorded.Load() != 5 {
		t.Fatalf("each keyed change must build one row: %d", recorded.Load())
	}
	// Enabling w2 builds only its row.
	s.AutonomousWorkloads = []string{"w1", "w2"}
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	if rows := table.Rows(); len(rows) != 2 || recordedW2.Load() != 1 || recorded.Load() != 6 {
		t.Fatalf("enabling w2 must add exactly its row: %+v, records %d", rows, recorded.Load())
	}
}

func TestCapacityTableBuildsEveryCompatibleVariant(t *testing.T) {
	s, snap := capacityFixture()
	baseWorkload, baseOption := *snap.Workloads[0], snap.Options[0]
	s.AutonomousWorkloads, snap.Workloads, snap.Options = nil, nil, nil
	snap.DispatchOccupancy = nil
	expected := map[string]string{}
	for i, count := range []int{3, 1, 2} {
		w := baseWorkload
		w.UID = string(rune('a' + i))
		s.AutonomousWorkloads = append(s.AutonomousWorkloads, w.UID)
		snap.Workloads = append(snap.Workloads, &w)
		for batch := 1; batch <= count; batch++ {
			o, p := baseOption, *baseOption.Instance.Profile
			p.ID = w.UID + string(rune('0'+batch))
			p.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: batch}, []float64{float64(w.SLO.TargetLatency) + 1})}
			o.Instance.Profile, o.Instance.MaxBatch = &p, batch
			o.CompatibleUIDs = []string{w.UID}
			snap.Options = append(snap.Options, o)
			expected[p.ID] = w.UID
		}
	}
	var recorded atomic.Int32
	table := NewCapacityTable(2, func(CapacityRow) error {
		recorded.Add(1)
		return nil
	}, nil)
	table.Ensure(context.Background(), s, snap)
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	rows := table.Rows()
	if len(rows) != len(expected) || int(recorded.Load()) != len(expected) {
		t.Fatalf("compatible variants must all build once: rows=%+v records=%d", rows, recorded.Load())
	}
	for _, row := range rows {
		if row.Status != CapacityBuilt || expected[row.Input.ProfileID] != row.Input.WorkloadUID {
			t.Fatalf("missing or incompatible built variant: %+v", row)
		}
		delete(expected, row.Input.ProfileID)
	}
	if len(expected) != 0 {
		t.Fatalf("compatible variants did not finish: %v", expected)
	}
}

// A recorded row stands in for a row being computed and gives way once built.
func TestCapacityTableUsesRecordedRowUntilBuilt(t *testing.T) {
	s, snap := capacityFixture()

	recordedKappa := 1.0
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, func(_ context.Context, keys []string) (map[string]CapacityRow, error) {
		return map[string]CapacityRow{keys[0]: {Key: keys[0], Status: CapacityBuilt, Kappa: &recordedKappa}}, nil
	}))
	table.Ensure(context.Background(), s, snap)
	running := nextCapacityBuild(t, started)
	waitCapacityLoad(t, table)
	if row := table.Rows()[0]; !row.Previous || row.Kappa == nil || *row.Kappa != recordedKappa {
		t.Fatalf("recorded row must stand in until rebuilt: %+v", row)
	}
	close(running.release)
	table.Wait()
	if row := table.Rows()[0]; row.Previous || row.Kappa == nil || *row.Kappa <= recordedKappa {
		t.Fatalf("the built row must replace the recorded one: %+v", row)
	}
}

func TestCapacityConfiguredNumerics(t *testing.T) {
	s, snap := capacityFixture()
	margin := s.CompletionMarginMS
	omitted, err := (SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &margin}).Settings()
	if err != nil || omitted.CapacityGridSteps != 20 || omitted.CapacityBisectionWidth != .005 || omitted.CapacityTailSamples != 100 {
		t.Fatalf("historical defaults: %+v %v", omitted, err)
	}
	explicit, err := (SchedulerSettingsInput{SchedulerSettings: s, CompletionMarginMS: &margin, CapacityGridSteps: &s.CapacityGridSteps, CapacityBisectionWidth: &s.CapacityBisectionWidth, CapacityTailSamples: &s.CapacityTailSamples}).Settings()
	if err != nil {
		t.Fatal(err)
	}
	a, b := capacityBuildForTest(t, omitted, snap), capacityBuildForTest(t, explicit, snap)
	if a.Kappa == nil || b.Kappa == nil || *a.Kappa != *b.Kappa || !reflect.DeepEqual(a.Grid, b.Grid) || !reflect.DeepEqual(a.Bisection, b.Bisection) {
		t.Fatal("default calculation changed")
	}
	for _, change := range []func(*SchedulerSettings){
		func(s *SchedulerSettings) { s.CapacityGridSteps = 4 },
		func(s *SchedulerSettings) { s.CapacityBisectionWidth = .05 },
		func(s *SchedulerSettings) { s.CapacityTailSamples = 10 },
	} {
		selected := s
		change(&selected)
		row := capacityBuildForTest(t, selected, snap)
		if row.Key == a.Key || reflect.DeepEqual(row.Grid, a.Grid) && reflect.DeepEqual(row.Bisection, a.Bisection) {
			t.Fatal("selected value did not change row calculation")
		}
		if row.Input.GridSteps != selected.CapacityGridSteps || row.Input.BisectionWidth != selected.CapacityBisectionWidth || row.Input.TailSamples != selected.CapacityTailSamples {
			t.Fatal("row lost selected policy")
		}
	}
	for _, change := range []func(*SchedulerSettings){
		func(s *SchedulerSettings) { s.CapacityGridSteps = 0 },
		func(s *SchedulerSettings) { s.CapacityTailSamples = 0 },
		func(s *SchedulerSettings) { s.CapacityBisectionWidth = 0 },
		func(s *SchedulerSettings) { s.CapacityBisectionWidth = 1.01 },
		func(s *SchedulerSettings) { s.CapacityBisectionWidth = math.NaN() },
		func(s *SchedulerSettings) { s.CapacityBisectionWidth = math.Inf(1) },
	} {
		invalid := s
		change(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("invalid numerical policy accepted")
		}
	}
}

func TestCapacityKeyIgnoresOccupancyImageHistory(t *testing.T) {
	in := CapacityInput{ProfileID: "profile", DispatchOccupancy: &DispatchOccupancy{RuntimeImage: "old-image", Model: "model"}}
	key := in.Key()
	in.DispatchOccupancy.RuntimeImage = "new-image"
	if key != in.Key() || in.DispatchOccupancy.RuntimeImage != "new-image" {
		t.Fatal("runtime image changed cache identity or key erased measurement history")
	}
	in.DispatchOccupancy.Model = "other-model"
	if key == in.Key() {
		t.Fatal("model condition disappeared from capacity key")
	}
}

func TestCapacityKeyIgnoresOccupancyProvenance(t *testing.T) {
	s, snap := capacityFixture()
	in := NewCapacityInput(s, nil, snap.Options[0], snap.Workloads[0], ResolvePathBudget(s, snap.PathBudgets, snap.Workloads[0]))
	key := in.Key()
	in.DispatchOccupancy.Source = "new-record-key"
	in.DispatchOccupancy.Provenance.Source = occupancySourceDOM
	if key != in.Key() {
		t.Fatal("source-only change invalidated numerical capacity")
	}
	in.DispatchOccupancy.PerRequest++
	if key == in.Key() {
		t.Fatal("numerical default d change did not invalidate capacity")
	}
}

func TestCapacityKeyAndTableReuseIgnoreProvenanceRefresh(t *testing.T) {
	s, snap := capacityFixture()
	i := snap.Options[0].Instance
	window := OccupancyWindow{Start: snap.At.Add(-time.Minute), End: snap.At}
	snap.DispatchOccupancy = []DispatchOccupancy{{
		Node: i.Node, PowerMode: i.Profile.Condition.PowerMode, Model: i.Profile.Condition.LogicalModel,
		ProfileID: i.Profile.ID, ExecutionPath: i.Profile.Condition.Artifact.ExecutionPath, Batch: i.MaxBatch,
		Source: "cache-before", Provenance: &DispatchOccupancyProvenance{Source: "measured", Pairs: 1000, Window: &window, UpdatedAt: snap.At},
	}}
	var recorded int
	table := NewCapacityTable(1, func(CapacityRow) error { recorded++; return nil }, nil)
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	before := table.Rows()[0]
	refreshedWindow := OccupancyWindow{Start: window.Start.Add(time.Second), End: window.End.Add(time.Second)}
	snap.DispatchOccupancy[0].Source = "cache-after"
	snap.DispatchOccupancy[0].Provenance = &DispatchOccupancyProvenance{Source: "measured", Pairs: 2000, Window: &refreshedWindow, UpdatedAt: snap.At.Add(time.Second)}
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	after := table.Rows()[0]
	if recorded != 1 || before.Key != after.Key || !before.BuiltAt.Equal(after.BuiltAt) || *before.Kappa != *after.Kappa ||
		!reflect.DeepEqual(after.Input.DispatchOccupancy.Provenance, snap.DispatchOccupancy[0].Provenance) ||
		after.Input.DispatchOccupancy.Source != "cache-after" || before.Input.DispatchOccupancy.Provenance.Pairs != 1000 {
		t.Fatalf("refresh rebuilt κ or lost current/original provenance: %+v %+v records=%d", before, after, recorded)
	}
	after.Input.DispatchOccupancy.PerRequest++
	if after.Input.Key() == before.Key {
		t.Fatal("numerical d change failed to invalidate capacity")
	}
}

func TestConfiguredDispatchOccupancyMultipliesBatchOnceInReplay(t *testing.T) {
	settings, snapshot := capacityFixture()
	settings.DefaultDispatchOccupancyNanos = 500000
	snapshot.DispatchOccupancy = nil
	snapshot.Instances[0].MaxBatch = 4
	snapshot.Instances[0].Profile.Batches = []BatchProfile{schedulerTestBatchProfile(BatchProfile{Batch: 4}, []float64{4e6})}
	state := schedulerReplayState{settings: settings, snap: snapshot, rng: rand.New(rand.NewPCG(1, 0)), now: 100}
	state.initializeInstances()
	instance := state.instances[0]
	for n := range 4 {
		instance.queue = append(instance.queue, &schedulerReplayRequest{
			source:   SchedulerRequest{ID: string(rune('a' + n)), Definition: snapshot.Workloads[0]},
			identity: snapshot.Workloads[0].Identity(), queueAt: state.now,
		})
	}
	if !state.chooseBatches(context.Background()) || len(instance.batch) != 4 || instance.finish != 100+4e6+4*500000 {
		t.Fatalf("replay must add B*d once, not a batch total or B²*d: %+v", instance)
	}
}
