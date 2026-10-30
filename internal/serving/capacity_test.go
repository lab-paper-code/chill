package serving

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func capacityFixture() (SchedulerSettings, SchedulerSnapshot) {
	s, snap := schedulerFixture()
	s.CapacityWindow, s.CapacityWindows, s.CapacityPassingWindows, s.CapacityMinimumRequests = time.Second, 3, 2, 200
	s.CompletionMarginMS = 0
	c := snap.Instances[0].Profile.Condition
	snap.DispatchOccupancy = []DispatchOccupancy{{Node: snap.Nodes[0].Node, PowerMode: c.PowerMode, RuntimeImage: c.RuntimeImage, Model: c.LogicalModel, Source: "fixture"}}
	p := *snap.Instances[0].Profile
	p.Batches = []BatchProfile{{Batch: 1, DurationNanos: []float64{8e5, 1e6, 1.2e6}, EnergyPerInputJoules: 0.01, MemoryBytes: 100}}
	snap.Instances[0].Profile = &p
	snap.Options[0].Instance = snap.Instances[0]
	snap.Workloads[0].SLO = SLO{Percentile: 95, TargetLatency: 20 * time.Millisecond}
	return s, snap
}

func capacityBuildForTest(t *testing.T, s SchedulerSettings, snap SchedulerSnapshot) CapacityRow {
	t.Helper()
	row, err := BuildCapacityRow(context.Background(), s, snap.DispatchOccupancy, snap.Options[0], snap.Workloads[0], snap.Nodes[0], snap.Baselines, snap.At)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// The fixture kappa is a constant: a replay or batching change that moves it
// fails here and needs a reviewed new constant.
func TestCapacityRowKappaIsFixedAndRepeatable(t *testing.T) {
	s, snap := capacityFixture()
	row := capacityBuildForTest(t, s, snap)
	if row.Status != CapacityBuilt || row.Kappa == nil || math.Abs(row.Mu-1000) > 1e-9 || math.Abs(*row.Kappa-capacityFixtureKappa) > 1e-9 {
		t.Fatalf("fixture row changed: status %s mu %g kappa %v", row.Status, row.Mu, *row.Kappa)
	}
	again := capacityBuildForTest(t, s, snap)
	if *again.Kappa != *row.Kappa || !reflect.DeepEqual(again.Grid, row.Grid) || !reflect.DeepEqual(again.Bisection, row.Bisection) || again.Key != row.Key {
		t.Fatal("the same row built twice differs")
	}
}

const capacityFixtureKappa = 959.375

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
				{Batch: 1, DurationNanos: tc.samples, MemoryBytes: 100},
				{Batch: 2, DurationNanos: tc.samples, MemoryBytes: 100},
			}
			snap.Options[0].Instance.MaxBatch = 2
			if tc.smaller {
				p.Batches[0].DurationNanos = []float64{1e6}
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
			snap.Instances[0].Profile.Batches[0].DurationNanos = []float64{tc.service}
			d := snap.Workloads[0]
			d.SLO = SLO{Percentile: tc.percentile, TargetLatency: 20 * time.Millisecond}
			keep := SchedulerCandidate{Keep: true, Nodes: snap.Nodes, Instances: snap.Instances}
			fullCtx := &capacityCountingContext{Context: context.Background()}
			full, err := schedulerReplayEstimate(fullCtx, s, snap, keep, 1, nil, nil)
			if err != nil || len(full.Reasons) != 0 {
				t.Fatalf("full replay failed: %v %+v", err, full)
			}
			limit := capacityWindowLimit{requests: n, slo: d.SLO}
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
	limit := capacityWindowLimit{requests: 20, slo: SLO{Percentile: 95, TargetLatency: time.Millisecond}}
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

// Without a measurement for the option's Node, power mode, runtime image and
// model the row has no kappa, even when another Node, mode, image or model has one.
func TestCapacityRowWithoutOccupancyHasNoKappa(t *testing.T) {
	s, snap := capacityFixture()
	snap.DispatchOccupancy[0].Node.UID = schedulerTestOther
	row := capacityBuildForTest(t, s, snap)
	if row.Status != CapacityRejected || row.Reason != CapacityNoOccupancy || row.Kappa != nil || len(row.Grid) != 0 || row.Input.DispatchOccupancy != nil {
		t.Fatalf("row without occupancy: %+v", row)
	}
	snap.DispatchOccupancy[0].Node = snap.Nodes[0].Node
	snap.DispatchOccupancy[0].PowerMode = schedulerTestEcoMode
	if row := capacityBuildForTest(t, s, snap); row.Kappa != nil || row.Reason != CapacityNoOccupancy {
		t.Fatalf("an occupancy measured in another mode gave kappa: %+v", row)
	}
	snap.DispatchOccupancy[0].PowerMode = snap.Instances[0].Profile.Condition.PowerMode
	snap.DispatchOccupancy[0].RuntimeImage = "registry/model@sha256:other"
	if row := capacityBuildForTest(t, s, snap); row.Kappa != nil {
		t.Fatalf("an occupancy measured on another runtime image gave kappa: %+v", row)
	}
	snap.DispatchOccupancy[0].RuntimeImage = snap.Instances[0].Profile.Condition.RuntimeImage
	snap.DispatchOccupancy[0].Model = "other-model"
	if row := capacityBuildForTest(t, s, snap); row.Kappa != nil {
		t.Fatalf("an occupancy measured with another model gave kappa: %+v", row)
	}
}

// rasp5 q1 at b1 (E1 profile, 903 samples, mean 27.79 ms) sustained 26.88
// req/s when saturated (m4 r3 Q2), so its occupancy is 37.20 − 27.79 =
// 9.41 ms. With it the tight row (p95 1 s, m 29 ms, live capacity settings)
// stays below the measured throughput; without it kappa was 34.3.
func TestCapacityRasp5KappaWithMeasuredOccupancy(t *testing.T) {
	if testing.Short() {
		t.Skip("replays full 60 s windows")
	}
	raw, err := os.ReadFile("testdata/rasp5-q1-b1-durations.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples struct {
		DurationNanos []float64 `json:"durationNanos"`
	}
	if err := json.Unmarshal(raw, &samples); err != nil {
		t.Fatal(err)
	}
	s, snap := capacityFixture()
	s.CapacityWindow, s.CapacityWindows, s.CapacityPassingWindows, s.CapacityMinimumRequests = time.Minute, 20, 19, 1000
	s.CompletionMarginMS = 29
	snap.DispatchOccupancy[0].PerRequest = 9409 * time.Microsecond
	snap.Instances[0].Profile.Batches = []BatchProfile{{Batch: 1, DurationNanos: samples.DurationNanos, EnergyPerInputJoules: 0.07, MemoryBytes: 100}}
	snap.Options[0].Instance = snap.Instances[0]
	snap.Workloads[0].SLO = SLO{Percentile: 95, TargetLatency: time.Second}
	row := capacityBuildForTest(t, s, snap)
	if row.Status != CapacityBuilt || math.Abs(row.Mu-26.88) > 0.01 || *row.Kappa < 25 || *row.Kappa > 26.88 {
		t.Fatalf("rasp5 q1 tight row: status %s mu %g kappa %v", row.Status, row.Mu, row.Kappa)
	}
	t.Logf("rasp5 q1 tight: mu %.2f kappa %.2f req/s", row.Mu, *row.Kappa)
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
func capacityStub(limit, resume float64, fail map[int]bool) func(context.Context, float64, int, int) (float64, bool, error) {
	return func(_ context.Context, lambda float64, _, seed int) (float64, bool, error) {
		return 0, (lambda <= limit || resume > 0 && lambda >= resume) && !fail[seed], nil
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
	var recorded atomic.Int32
	table := NewCapacityTable(2, func(row CapacityRow) error {
		if row.Input.WorkloadUID != "w1" {
			t.Errorf("row for disabled Workload %s", row.Input.WorkloadUID)
		}
		recorded.Add(1)
		return nil
	}, nil)
	table.parallel <- struct{}{}
	table.parallel <- struct{}{}
	table.Ensure(context.Background(), s, snap)
	if rows := table.Rows(); len(rows) != 1 || rows[0].Status != CapacityPending || rows[0].Kappa != nil {
		t.Fatalf("a row being computed must be CapacityPending: %+v", rows)
	}
	<-table.parallel
	<-table.parallel
	table.Wait()
	if rows := table.Rows(); len(rows) != 1 || rows[0].Status != CapacityBuilt || recorded.Load() != 1 {
		t.Fatalf("one built and recorded row: %+v, records %d", rows, recorded.Load())
	}
	// Inputs outside the row key start nothing.
	snap.At = snap.At.Add(time.Minute)
	s.ControlInterval, s.MaxReplicas = 2*s.ControlInterval, s.MaxReplicas+1
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	if recorded.Load() != 1 {
		t.Fatalf("unrelated change recomputed: %d", recorded.Load())
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
	recordedW2 := 0
	table.record = func(row CapacityRow) error {
		if row.Input.WorkloadUID == "w2" {
			recordedW2++
		}
		return nil
	}
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	if rows := table.Rows(); len(rows) != 2 || recordedW2 != 1 {
		t.Fatalf("enabling w2 must add exactly its row: %+v, %d", rows, recordedW2)
	}
}

// Hold the first Err call inside a running build until Ensure cancels it.
type capacityBlockedContext struct {
	context.Context
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *capacityBlockedContext) Err() error {
	c.once.Do(func() {
		close(c.started)
		<-c.release
	})
	return c.Context.Err()
}

func TestCapacityTableCancelsUnwantedQueuedAndRunning(t *testing.T) {
	s, snap := capacityFixture()
	p := *snap.Options[0].Instance.Profile
	p.ID += "-queued"
	o := snap.Options[0]
	o.Instance.Profile = &p
	snap.Options = append(snap.Options, o)
	var recorded atomic.Int32
	table := NewCapacityTable(1, func(CapacityRow) error {
		recorded.Add(1)
		return nil
	}, nil)
	table.parallel <- struct{}{}
	table.Ensure(context.Background(), s, snap)
	key := NewCapacityInput(s, snap.DispatchOccupancy, snap.Options[0], snap.Workloads[0]).Key()
	queuedKey := NewCapacityInput(s, snap.DispatchOccupancy, snap.Options[1], snap.Workloads[0]).Key()
	table.mu.Lock()
	running, queued := table.running[key], table.running[queuedKey]
	original := running.ctx
	blocked := &capacityBlockedContext{Context: original, started: make(chan struct{}), release: make(chan struct{})}
	running.ctx = blocked
	table.mu.Unlock()
	<-table.parallel
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("build did not start")
	}
	empty := snap
	empty.Options = nil
	table.Ensure(context.Background(), s, empty)
	if original.Err() != context.Canceled || queued.ctx.Err() != context.Canceled {
		t.Fatal("Ensure must cancel both running and queued obsolete keys")
	}
	// Re-add the same keys before the old build exits: its cleanup must not
	// delete the replacement task or publish its canceled result.
	table.Ensure(context.Background(), s, snap)
	close(blocked.release)
	done := make(chan struct{})
	go func() { table.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled tasks retained the only replay slot")
	}
	if rows := table.Rows(); len(rows) != 2 || rows[0].Status != CapacityBuilt || rows[1].Status != CapacityBuilt || recorded.Load() != 2 {
		t.Fatalf("only replacement rows should be recorded: %+v, records %d", rows, recorded.Load())
	}
}

func TestCapacityTableRoundRobinWorkloads(t *testing.T) {
	s, snap := capacityFixture()
	baseWorkload, baseOption := *snap.Workloads[0], snap.Options[0]
	s.AutonomousWorkloads, snap.Workloads, snap.Options = nil, nil, nil
	snap.DispatchOccupancy = nil // Reject without replay; only queue order matters.
	for i, count := range []int{3, 1, 2} {
		w := baseWorkload
		w.UID = string(rune('a' + i))
		s.AutonomousWorkloads = append(s.AutonomousWorkloads, w.UID)
		snap.Workloads = append(snap.Workloads, &w)
		// Multiple options exercise advancing from one option to the next.
		for batch := 1; batch <= count; batch++ {
			o, p := baseOption, *baseOption.Instance.Profile
			p.ID = w.UID + string(rune('0'+batch))
			p.Batches = []BatchProfile{{Batch: batch}}
			o.Instance.Profile, o.Instance.MaxBatch = &p, batch
			o.CompatibleUIDs = []string{w.UID}
			snap.Options = append(snap.Options, o)
		}
	}
	var order []string
	table := NewCapacityTable(1, func(row CapacityRow) error {
		order = append(order, row.Input.ProfileID)
		return nil
	}, nil)
	table.parallel <- struct{}{}
	table.Ensure(context.Background(), s, snap)
	table.Ensure(context.Background(), s, snap) // Queued rows must not duplicate.
	<-table.parallel
	table.Wait()
	if want := []string{"a1", "b1", "c1", "a2", "c2", "a3"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("serial row execution = %v, want %v", order, want)
	}
}

// A recorded row stands in for a row being computed and gives way once built.
func TestCapacityTableUsesRecordedRowUntilBuilt(t *testing.T) {
	s, snap := capacityFixture()
	key := NewCapacityInput(s, snap.DispatchOccupancy, snap.Options[0], snap.Workloads[0]).Key()
	recordedKappa := 1.0
	table := NewCapacityTable(1, nil, func(_ context.Context, keys []string) (map[string]CapacityRow, error) {
		if len(keys) != 1 || keys[0] != key {
			t.Errorf("load asked %v", keys)
		}
		return map[string]CapacityRow{key: {Key: key, Status: CapacityBuilt, Kappa: &recordedKappa}}, nil
	})
	table.parallel <- struct{}{}
	table.Ensure(context.Background(), s, snap)
	deadline := time.Now().Add(5 * time.Second)
	for rows := table.Rows(); !rows[0].Previous; rows = table.Rows() {
		if time.Now().After(deadline) {
			t.Fatalf("recorded row never stood in: %+v", rows)
		}
		time.Sleep(time.Millisecond)
	}
	if row := table.Rows()[0]; *row.Kappa != recordedKappa {
		t.Fatalf("recorded kappa lost: %+v", row)
	}
	<-table.parallel
	table.Wait()
	if row := table.Rows()[0]; row.Previous || *row.Kappa != capacityFixtureKappa {
		t.Fatalf("the built row must replace the recorded one: %+v", row)
	}
}

func TestCapacityRecordsReturnLatestRowPerKey(t *testing.T) {
	observationLog, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	for _, row := range []CapacityRow{{Key: "a", Mu: 1}, {Key: "b", Mu: 2}, {Key: "a", Mu: 3}} {
		if err := observationLog.ManagementEvent(ManagementEvent{WorkloadUID: "w1", Kind: CapacityRowBuiltEvent, At: observationTestHour, Capacity: &row}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := observationLog.CapacityRecords(context.Background(), []string{"a", "c"})
	if err != nil || len(rows) != 1 || rows["a"].Mu != 3 {
		t.Fatalf("latest recorded row per asked key: %+v %v", rows, err)
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
	if *a.Kappa != capacityFixtureKappa || !reflect.DeepEqual(a.Grid, b.Grid) || !reflect.DeepEqual(a.Bisection, b.Bisection) {
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
