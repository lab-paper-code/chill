package serving

import (
	"context"
	"math"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func capacityFixture() (SchedulerSettings, SchedulerSnapshot) {
	s, snap := schedulerFixture()
	s.CapacityWindow, s.CapacityWindows, s.CapacityPassingWindows, s.CapacityMinimumRequests = time.Second, 3, 2, 200
	s.CompletionMarginMS = 0
	p := *snap.Instances[0].Profile
	p.Batches = []BatchProfile{{Batch: 1, DurationNanos: []float64{8e5, 1e6, 1.2e6}, EnergyPerInputJoules: 0.01, MemoryBytes: 100}}
	snap.Instances[0].Profile = &p
	snap.Options[0].Instance = snap.Instances[0]
	snap.Workloads[0].SLO = SLO{Percentile: 95, TargetLatency: 20 * time.Millisecond}
	return s, snap
}

func capacityBuildForTest(t *testing.T, s SchedulerSettings, snap SchedulerSnapshot) CapacityRow {
	t.Helper()
	row, err := BuildCapacityRow(context.Background(), s, snap.Options[0], snap.Workloads[0], snap.Nodes[0], snap.Baselines, snap.At)
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
	if row := capacityBuildForTest(t, s, snap); row.Kappa == nil || *row.Kappa != 0 || len(row.Grid) != capacityGridSteps {
		t.Fatalf("margin 139 ms, p99 100 ms: %+v", row)
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
	s.ControlInterval, s.MaxCandidates = 2*s.ControlInterval, s.MaxCandidates+1
	table.Ensure(context.Background(), s, snap)
	table.Wait()
	if recorded.Load() != 1 {
		t.Fatalf("unrelated change recomputed: %d", recorded.Load())
	}
	// SLO, margin, profile and capacity settings each add one missing row.
	snap.Workloads[0].SLO.TargetLatency = 30 * time.Millisecond
	table.Ensure(context.Background(), s, snap)
	s.CompletionMarginMS = 5
	table.Ensure(context.Background(), s, snap)
	p := *snap.Options[0].Instance.Profile
	p.ID = "profile-2"
	snap.Options[0].Instance.Profile = &p
	table.Ensure(context.Background(), s, snap)
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

// A recorded row stands in for a row being computed and gives way once built.
func TestCapacityTableUsesRecordedRowUntilBuilt(t *testing.T) {
	s, snap := capacityFixture()
	key := NewCapacityInput(s, snap.Options[0], snap.Workloads[0]).Key()
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
