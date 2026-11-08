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
	table.Configure(settings)
	if rows, budgets := table.Inputs(); len(rows) != 0 || len(budgets) != 0 {
		t.Fatalf("incompatible groups survived accepted OFF policy: rows=%+v budgets=%+v", rows, budgets)
	}
	if obsolete.ctx.Err() != context.Canceled {
		t.Fatal("disabled observed-only build remained admitted")
	}
	close(obsolete.release)
	table.Wait()
	select {
	case boundary := <-started:
		t.Fatalf("accepted OFF policy admitted old queued observation: %+v", boundary.budget)
	default:
	}
	table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 999))
	for range 2 {
		boundary := nextCapacityBuild(t, started)
		assertConfiguredCapacityInputs(t, table, 7, 2)
		if boundary.budget.TotalMS != 7 || boundary.budget.PostMS != 7 {
			t.Fatalf("observed successor survived policy cutover: %+v", boundary.budget)
		}
		close(boundary.release)
	}
	table.Wait()
	assertCapacityGroup(t, table, 7, 2, CapacityBuilt)
	settings.CompletionMarginMS = 9
	table.Ensure(context.Background(), settings, snapshot)
	assertNoActiveCapacityGroup(t, table, 9, 2)
	for range 2 {
		close(nextCapacityBuild(t, started).release)
	}
	table.Wait()
	assertCapacityGroup(t, table, 9, 2, CapacityBuilt)
}

func assertConfiguredCapacityInputs(t *testing.T, table *CapacityTable, margin float64, variants int) []CapacityRow {
	t.Helper()
	rows, budgets := table.Inputs()
	if len(budgets) == 0 {
		rows = assertNoActiveCapacityGroup(t, table, margin, variants)
	} else if len(rows) != variants || len(budgets) != 1 || budgets[0].Source != PathBudgetConfigured ||
		budgets[0].TotalMS != margin || budgets[0].PostMS != margin || budgets[0].JudgmentPercentile != 97.5 {
		t.Fatalf("configured pair not published: rows=%+v budgets=%+v", rows, budgets)
	}
	for _, row := range rows {
		if row.Input.CompletionMarginMS != margin || row.Input.PostMarginMS != margin ||
			row.Input.JudgmentPercentile != 97.5 || row.Input.SLO.Percentile != 95 ||
			row.Status != CapacityBuilt && row.Status != CapacityPending {
			t.Fatalf("row does not use configured pair/native percentile: %+v", row)
		}
	}
	return rows
}

func TestCapacityTableDisabledPolicyPreservesMatchingActiveRows(t *testing.T) {
	for _, boundary := range []string{"configure", "ensure"} {
		t.Run(boundary, func(t *testing.T) {
			settings, snapshot := capacityLifecycleFixture(2)
			matching := capacityLifecycleBudgets(snapshot, 7)
			matching.PathBudgets[0].PostMS = 7
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
			table.Ensure(context.Background(), settings, matching)
			for range 2 {
				close(nextCapacityBuild(t, started).release)
			}
			table.Wait()
			before := table.Rows()
			table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 10))
			obsolete := nextCapacityBuild(t, started)
			enabled := false
			settings.ObservedPathBudgets, settings.CompletionMarginMS = &enabled, 7
			if boundary == "configure" {
				table.Configure(settings)
			} else {
				table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 999))
			}
			if obsolete.ctx.Err() != context.Canceled {
				t.Fatal("incompatible observed build was not canceled")
			}
			if !reflect.DeepEqual(before, assertConfiguredCapacityInputs(t, table, 7, 2)) {
				t.Fatal("matching active rows lost numerical result, key, or BuiltAt")
			}
			close(obsolete.release)
			table.Wait()
			table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 999))
			if !reflect.DeepEqual(before, table.Rows()) || table.WorkerStatus().Active != 0 {
				t.Fatal("next real capture rebuilt matching configured rows")
			}
			select {
			case task := <-started:
				t.Fatalf("matching active group rebuilt unnecessarily: %+v", task.budget)
			default:
			}
		})
	}
}

func TestCapacityTableDisabledPolicyPreservesMatchingBuildingWork(t *testing.T) {
	for _, boundary := range []string{"configure", "ensure"} {
		t.Run(boundary, func(t *testing.T) {
			settings, snapshot := capacityLifecycleFixture(3)
			matching := capacityLifecycleBudgets(snapshot, 7)
			matching.PathBudgets[0].PostMS = 7
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
			table.Ensure(context.Background(), settings, matching)
			first := nextCapacityBuild(t, started)
			close(first.release)
			running := nextCapacityBuild(t, started)
			before := table.Rows()[0]
			frozen := running.budget
			table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 10))
			enabled := false
			settings.ObservedPathBudgets, settings.CompletionMarginMS = &enabled, 7
			if boundary == "configure" {
				table.Configure(settings)
			} else {
				table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 999))
			}
			if running.ctx.Err() != nil || !reflect.DeepEqual(frozen, running.budget) {
				t.Fatal("matching running task was canceled or its frozen inputs changed")
			}
			rows := assertConfiguredCapacityInputs(t, table, 7, 3)
			if before.Status != CapacityBuilt || !reflect.DeepEqual(before, rows[0]) {
				t.Fatal("matching building group lost its completed variant")
			}
			close(running.release)
			last := nextCapacityBuild(t, started)
			if last.batch == first.batch || last.batch == running.batch || last.budget.TotalMS != 7 || last.budget.PostMS != 7 {
				t.Fatalf("matching build restarted a variant or admitted disabled observation: %+v", last)
			}
			close(last.release)
			table.Wait()
			if !reflect.DeepEqual(before, table.Rows()[0]) {
				t.Fatal("completed matching result changed during policy cutover")
			}
			table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 999))
			select {
			case task := <-started:
				t.Fatalf("matching building group rebuilt unnecessarily: %+v", task.budget)
			default:
			}
		})
	}
}
