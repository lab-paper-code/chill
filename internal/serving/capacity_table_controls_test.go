package serving

import (
	"context"
	"reflect"
	"testing"
)

func TestCapacityTableDisabledObservationsDoNotBuildNewRows(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(2)
	enabled := false
	settings.ObservedPathBudgets, settings.CompletionMarginMS = &enabled, 7
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
	snapshot = capacityLifecycleBudgets(snapshot, 100)
	table.Ensure(context.Background(), settings, snapshot)
	for range 2 {
		boundary := nextCapacityBuild(t, started)
		if boundary.budget.TotalMS != 7 || boundary.budget.PostMS != 7 || boundary.budget.JudgmentPercentile != 97.5 {
			t.Fatalf("disabled observation reached builder: %+v", boundary.budget)
		}
		close(boundary.release)
	}
	table.Wait()
	before := table.Rows()
	for _, observed := range []float64{0, 1, 999} {
		snapshot = capacityLifecycleBudgets(snapshot, observed)
		raw := append([]PathBudget(nil), snapshot.PathBudgets...)
		table.Ensure(context.Background(), settings, snapshot)
		assertCapacityGroup(t, table, 7, 2, CapacityBuilt)
		if !reflect.DeepEqual(raw, snapshot.PathBudgets) || !reflect.DeepEqual(before, table.Rows()) {
			t.Fatal("disabled observation changed raw evidence or numerical rows")
		}
		select {
		case boundary := <-started:
			t.Fatalf("disabled observation started more work: %+v", boundary.budget)
		default:
		}
	}
}

func TestCapacityTableDisablingObservationsDropsActiveAndRunningBudgets(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(2)
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 1))
	for range 2 {
		close(nextCapacityBuild(t, started).release)
	}
	table.Wait()
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 2))
	obsolete := nextCapacityBuild(t, started)
	obsolete.ignoreCancellation = true
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 3))
	enabled := false
	settings.ObservedPathBudgets, settings.CompletionMarginMS = &enabled, 7
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 999))
	assertCapacityGroup(t, table, 7, 2, CapacityPending)
	if obsolete.ctx.Err() != context.Canceled {
		t.Fatal("disabled observed-only build remained admitted")
	}
	close(obsolete.release)
	for range 2 {
		boundary := nextCapacityBuild(t, started)
		assertCapacityGroup(t, table, 7, 2, CapacityPending)
		if boundary.budget.TotalMS != 7 || boundary.budget.PostMS != 7 {
			t.Fatalf("observed successor survived policy cutover: %+v", boundary.budget)
		}
		close(boundary.release)
	}
	table.Wait()
	assertCapacityGroup(t, table, 7, 2, CapacityBuilt)
	settings.CompletionMarginMS = 9
	table.Ensure(context.Background(), settings, snapshot)
	assertCapacityGroup(t, table, 9, 2, CapacityPending)
	for range 2 {
		close(nextCapacityBuild(t, started).release)
	}
	table.Wait()
	assertCapacityGroup(t, table, 9, 2, CapacityBuilt)
}
