package serving

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// Gates surround the real numerical builder. These tests control admission and
// completion without replacing the table's queue or publication state machine.
type capacityBuildBoundary struct {
	ctx                context.Context
	budget             PathBudget
	uid                string
	batch              int
	release            chan struct{}
	ignoreCancellation bool
}

func newCapacityLifecycleTable(t *testing.T, parallel int) (*CapacityTable, <-chan *capacityBuildBoundary, <-chan struct{}) {
	t.Helper()
	started := make(chan *capacityBuildBoundary, 32)
	stop := make(chan struct{})
	table := NewCapacityTable(parallel, nil, nil)
	table.buildRow = func(ctx context.Context, settings SchedulerSettings, occupancy []DispatchOccupancy, option SchedulerOption, w *Definition, at time.Time, budget PathBudget) (CapacityRow, error) {
		boundary := &capacityBuildBoundary{ctx: ctx, budget: budget, uid: w.UID, batch: option.Instance.MaxBatch, release: make(chan struct{})}
		select {
		case started <- boundary:
		case <-stop:
			return CapacityRow{}, context.Canceled
		}
		select {
		case <-boundary.release:
		case <-stop:
			return CapacityRow{}, context.Canceled
		}
		if boundary.ignoreCancellation {
			ctx = context.Background()
		}
		return BuildCapacityRow(ctx, settings, occupancy, option, w, at, budget)
	}
	t.Cleanup(func() {
		table.Ensure(context.Background(), SchedulerSettings{}, SchedulerSnapshot{})
		close(stop)
		table.Wait()
	})
	return table, started, stop
}

func nextCapacityBuild(t *testing.T, started <-chan *capacityBuildBoundary) *capacityBuildBoundary {
	t.Helper()
	select {
	case boundary := <-started:
		return boundary
	case <-time.After(5 * time.Second):
		t.Fatal("capacity build did not reach its boundary")
		return nil
	}
}

func awaitCapacityBoundary(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("capacity lifecycle boundary did not finish")
	}
}

func waitCapacityLoad(t *testing.T, table *CapacityTable) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		table.mu.Lock()
		for table.loading != nil {
			table.idle.Wait()
		}
		table.mu.Unlock()
		close(done)
	}()
	awaitCapacityBoundary(t, done)
}

func capacityLifecycleFixture(variants int) (SchedulerSettings, SchedulerSnapshot) {
	settings, snapshot := capacityFixture()
	p := *snapshot.Options[0].Instance.Profile
	p.Batches = nil
	for batch := 1; batch <= variants; batch++ {
		// Analytical service>SLO rows keep lifecycle tests independent of replay cost.
		p.Batches = append(p.Batches, schedulerTestBatchProfile(BatchProfile{Batch: batch}, []float64{30e6}))
	}
	snapshot.Options[0].Instance.Profile = &p
	snapshot.Options[0].Instance.MaxBatch = variants
	return settings, snapshot
}

func capacityLifecycleBudgets(snapshot SchedulerSnapshot, values ...float64) SchedulerSnapshot {
	snapshot.PathBudgets = nil
	for i, w := range snapshot.Workloads {
		snapshot.PathBudgets = append(snapshot.PathBudgets, PathBudget{WorkloadUID: w.UID, DefinitionID: w.Identity(), Source: "observed",
			From: snapshot.At.Add(-time.Minute), To: snapshot.At, TotalMS: values[i], PostMS: values[i] / 2, JudgmentPercentile: 97.5})
	}
	return snapshot
}

func assertCapacityGroup(t *testing.T, table *CapacityTable, budget float64, variants int, status string) {
	t.Helper()
	rows, budgets := table.Inputs()
	if len(rows) != variants || len(budgets) != 1 || budgets[0].TotalMS != budget {
		t.Fatalf("incoherent group: rows=%+v budgets=%+v", rows, budgets)
	}
	for _, row := range rows {
		if row.Status != status || row.Input.CompletionMarginMS != budget || row.Input.PostMarginMS != budgets[0].PostMS ||
			row.Input.JudgmentPercentile != budgets[0].JudgmentPercentile {
			t.Fatalf("row does not match published budget: row=%+v budget=%+v", row, budgets[0])
		}
	}
}

func TestCapacityTableContinuousBudgetsFinishWholeGroups(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(2)
	table, started, _ := newCapacityLifecycleTable(t, 1)
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 1))
	first := nextCapacityBuild(t, started)
	for _, budget := range []float64{2, 3, 4} {
		table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, budget))
	}
	assertCapacityGroup(t, table, 4, 2, CapacityPending)
	if first.ctx.Err() != nil || first.budget.TotalMS != 1 {
		t.Fatal("budget drift canceled or changed admitted work")
	}
	close(first.release)
	second := nextCapacityBuild(t, started)
	if second.budget.TotalMS != 1 || second.batch == first.batch {
		t.Fatalf("running group lost its remaining variant: %+v", second)
	}
	assertCapacityGroup(t, table, 4, 2, CapacityPending)
	close(second.release)
	third := nextCapacityBuild(t, started)
	assertCapacityGroup(t, table, 1, 2, CapacityBuilt)
	if third.budget.TotalMS != 4 {
		t.Fatalf("queued observations did not coalesce to latest budget: %+v", third.budget)
	}
	for _, budget := range []float64{5, 6} {
		table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, budget))
	}
	waited := make(chan struct{})
	go func() { table.Wait(); close(waited) }()
	close(third.release)
	fourth := nextCapacityBuild(t, started)
	assertCapacityGroup(t, table, 1, 2, CapacityBuilt)
	if fourth.budget.TotalMS != 4 {
		t.Fatal("newest drift interrupted the intermediate group")
	}
	close(fourth.release)
	fifth := nextCapacityBuild(t, started)
	assertCapacityGroup(t, table, 4, 2, CapacityBuilt)
	if fifth.budget.TotalMS != 6 {
		t.Fatal("latest budget was not started after promotion")
	}
	close(fifth.release)
	last := nextCapacityBuild(t, started)
	select {
	case <-waited:
		t.Fatal("Wait returned before the final admitted work finished")
	default:
	}
	close(last.release)
	awaitCapacityBoundary(t, waited)
	assertCapacityGroup(t, table, 6, 2, CapacityBuilt)
}

func TestCapacityTableQueuedBudgetIncreasesPassDecreases(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(1)
	base := *snapshot.Workloads[0]
	snapshot.Workloads, settings.AutonomousWorkloads = nil, nil
	for _, uid := range []string{"a", "b", "c"} {
		w := base
		w.UID = uid
		snapshot.Workloads = append(snapshot.Workloads, &w)
		settings.AutonomousWorkloads = append(settings.AutonomousWorkloads, uid)
	}
	snapshot.Options[0].CompatibleUIDs = settings.AutonomousWorkloads
	table, started, _ := newCapacityLifecycleTable(t, 1)
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 10, 10, 10))
	for range snapshot.Workloads {
		close(nextCapacityBuild(t, started).release)
	}
	table.Wait()
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 9, 10, 10))
	running := nextCapacityBuild(t, started)
	if running.uid != "a" {
		t.Fatalf("unexpected admitted workload: %s", running.uid)
	}
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 9, 8, 12))
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 9, 7, 13))
	if running.ctx.Err() != nil {
		t.Fatal("queued upward work canceled an admitted decrease")
	}
	close(running.release)
	upward := nextCapacityBuild(t, started)
	if upward.uid != "c" || upward.budget.TotalMS != 13 {
		t.Fatalf("latest upward group must pass the queued decrease: %+v", upward)
	}
	close(upward.release)
	downward := nextCapacityBuild(t, started)
	if downward.uid != "b" || downward.budget.TotalMS != 7 {
		t.Fatalf("queued decrease did not coalesce: %+v", downward)
	}
	close(downward.release)
	table.Wait()
}

func TestCapacityTableUnchangedNumbersRefreshProvenanceWithoutRestart(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(1)
	table, started, _ := newCapacityLifecycleTable(t, 1)
	desired := capacityLifecycleBudgets(snapshot, 3)
	table.Ensure(context.Background(), settings, desired)
	running := nextCapacityBuild(t, started)
	originalBudget := running.budget
	desired.At = desired.At.Add(time.Second)
	desired.PathBudgets[0].To = desired.At
	desired.PathBudgets[0].Samples = 1000
	desired.DispatchOccupancy = append([]DispatchOccupancy(nil), desired.DispatchOccupancy...)
	desired.DispatchOccupancy[0].Source = "refreshed-evidence"
	table.Ensure(context.Background(), settings, desired)
	if running.ctx.Err() != nil || !reflect.DeepEqual(running.budget, originalBudget) {
		t.Fatal("provenance refresh changed the admitted task budget")
	}
	close(running.release)
	table.Wait()
	before := table.Rows()[0]
	table.Ensure(context.Background(), settings, desired)
	rows, budgets := table.Inputs()
	table.mu.Lock()
	busy := table.workers != 0 || len(table.running) != 0
	table.mu.Unlock()
	if busy || rows[0].Key != before.Key || rows[0].BuiltAt != before.BuiltAt || rows[0].Previous ||
		budgets[0].To != desired.At || rows[0].Input.DispatchOccupancy.Source != "refreshed-evidence" {
		t.Fatalf("unchanged numerical input restarted or lost provenance: rows=%+v budgets=%+v", rows, budgets)
	}
}

func TestCapacityTableCanceledSnapshotCanRestartUnchangedInputs(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(1)
	table, started, _ := newCapacityLifecycleTable(t, 1)
	desired := capacityLifecycleBudgets(snapshot, 1)
	ctx, cancel := context.WithCancel(context.Background())
	table.Ensure(ctx, settings, desired)
	canceled := nextCapacityBuild(t, started)
	cancel()
	close(canceled.release)
	table.Wait()
	assertCapacityGroup(t, table, 1, 1, CapacityPending)
	table.Ensure(context.Background(), settings, desired)
	replacement := nextCapacityBuild(t, started)
	if replacement.ctx.Err() != nil {
		t.Fatal("unchanged input retained its canceled admission context")
	}
	close(replacement.release)
	table.Wait()
	assertCapacityGroup(t, table, 1, 1, CapacityBuilt)
}

func TestCapacityTableStructuralRemovalDiscardsActiveAndOldTask(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(1)
	table, started, _ := newCapacityLifecycleTable(t, 2)
	var recorded atomic.Int32
	table.record = func(CapacityRow) error { recorded.Add(1); return nil }
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 1))
	close(nextCapacityBuild(t, started).release)
	table.Wait()
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 2))
	obsolete := nextCapacityBuild(t, started)
	obsolete.ignoreCancellation = true // A successful late build still cannot publish.
	empty := snapshot
	empty.Options = nil
	table.Ensure(context.Background(), settings, empty)
	rows, budgets := table.Inputs()
	if obsolete.ctx.Err() != context.Canceled || len(rows) != 0 || len(budgets) != 1 {
		t.Fatal("structural removal kept old work or its active inputs")
	}
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 2))
	replacement := nextCapacityBuild(t, started)
	assertCapacityGroup(t, table, 2, 1, CapacityPending)
	other := *snapshot.Workloads[0]
	other.UID = "other"
	snapshot.Workloads = append(snapshot.Workloads, &other)
	settings.AutonomousWorkloads = append(settings.AutonomousWorkloads, other.UID)
	snapshot.Options[0].CompatibleUIDs = append([]string(nil), settings.AutonomousWorkloads...)
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 2, 2))
	close(obsolete.release)
	// Admission proves the obsolete task released its slot and finished cleanup.
	third := nextCapacityBuild(t, started)
	if third.uid != other.UID || replacement.ctx.Err() != nil {
		t.Fatal("obsolete completion removed or canceled the replacement task")
	}
	for _, row := range table.Rows() {
		if row.Status != CapacityPending {
			t.Fatal("obsolete successful result leaked into replacement inputs")
		}
	}
	close(replacement.release)
	close(third.release)
	table.Wait()
	if recorded.Load() != 3 { // Initial group, replacement, and the other Workload.
		t.Fatalf("obsolete task published a record: %d", recorded.Load())
	}
}

func TestCapacityTableLateLoadCannotResurrectRemovedRows(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(1)
	table, started, stop := newCapacityLifecycleTable(t, 2)
	type loadBoundary struct {
		keys    []string
		release chan struct{}
	}
	loads := make(chan loadBoundary, 2)
	table.load = func(_ context.Context, keys []string) (map[string]CapacityRow, error) {
		boundary := loadBoundary{keys: keys, release: make(chan struct{})}
		select {
		case loads <- boundary:
		case <-stop:
			return nil, context.Canceled
		}
		select {
		case <-boundary.release:
		case <-stop:
			return nil, context.Canceled
		}
		kappa := 123.0
		return map[string]CapacityRow{keys[0]: {Key: keys[0], Status: CapacityBuilt, Kappa: &kappa}}, nil
	}
	nextLoad := func() loadBoundary {
		t.Helper()
		select {
		case boundary := <-loads:
			return boundary
		case <-time.After(5 * time.Second):
			t.Fatal("archive load did not start")
			return loadBoundary{}
		}
	}
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 1))
	obsoleteBuild, obsoleteLoad := nextCapacityBuild(t, started), nextLoad()
	empty := snapshot
	empty.Workloads = nil
	table.Ensure(context.Background(), settings, empty)
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 1))
	replacement := nextCapacityBuild(t, started)
	close(obsoleteLoad.release)
	currentLoad := nextLoad() // The old callback finished before this load started.
	if currentLoad.keys[0] != obsoleteLoad.keys[0] {
		t.Fatal("test did not recreate the removed numerical input")
	}
	row := table.Rows()[0]
	if row.Previous || row.Kappa != nil || row.Status != CapacityPending {
		t.Fatalf("old load resurrected a removed row: %+v", row)
	}
	close(currentLoad.release)
	waitCapacityLoad(t, table)
	if row = table.Rows()[0]; !row.Previous || row.Kappa == nil || *row.Kappa != 123 {
		t.Fatalf("current recorded row did not stand in for its rebuild: %+v", row)
	}
	close(obsoleteBuild.release)
	close(replacement.release)
	table.Wait()
	if row = table.Rows()[0]; row.Previous || row.Kappa == nil || *row.Kappa != 0 {
		t.Fatalf("recorded row survived the current build: %+v", row)
	}
}

func TestCapacityTableReclaimsUnreferencedRowsAndGroups(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(1)
	table, started, _ := newCapacityLifecycleTable(t, 1)
	table.load = func(_ context.Context, keys []string) (map[string]CapacityRow, error) {
		return map[string]CapacityRow{keys[0]: {Key: keys[0], Status: CapacityBuilt}}, nil
	}
	for budget := 1.0; budget <= 8; budget++ {
		table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, budget))
		boundary := nextCapacityBuild(t, started)
		waitCapacityLoad(t, table)
		close(boundary.release)
		table.Wait()
		assertCapacityGroup(t, table, budget, 1, CapacityBuilt)
		table.mu.Lock()
		state := table.groups[snapshot.Workloads[0].UID]
		retained := len(table.rows) == 1 && len(table.previous) == 0 && len(table.asked) == 1 && len(table.running) == 0 &&
			state.active == state.latest && state.building == nil
		table.mu.Unlock()
		if !retained {
			t.Fatal("completed transition retained obsolete numerical rows or groups")
		}
	}
	table.Ensure(context.Background(), settings, SchedulerSnapshot{})
	table.Wait()
	table.mu.Lock()
	defer table.mu.Unlock()
	if len(table.rows)+len(table.previous)+len(table.asked)+len(table.running)+len(table.groups)+len(table.order) != 0 || table.loading != nil {
		t.Fatal("removed Workload still retains capacity state")
	}
}
