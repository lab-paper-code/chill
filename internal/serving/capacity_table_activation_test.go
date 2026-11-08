package serving

import (
	"context"
	"reflect"
	"testing"
)

func TestCapacityTableInputsWaitForWholeInitialGroup(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(2)
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
	desired := capacityLifecycleBudgets(snapshot, 10)
	table.Ensure(context.Background(), settings, desired)
	first := nextCapacityBuild(t, started)
	for _, row := range assertNoActiveCapacityGroup(t, table, 10, 2) {
		if row.Status != CapacityPending {
			t.Fatalf("initial diagnostic row is not pending: %+v", row)
		}
	}
	close(first.release)
	second := nextCapacityBuild(t, started)
	rows := assertNoActiveCapacityGroup(t, table, 10, 2)
	if rows[0].Status != CapacityBuilt || rows[1].Status != CapacityPending {
		t.Fatalf("partial progress disappeared from management view: %+v", rows)
	}
	close(second.release)
	table.Wait()
	assertCapacityGroup(t, table, 10, 2)
	rows, budgets := table.Inputs()
	if !reflect.DeepEqual(budgets, desired.PathBudgets) || !reflect.DeepEqual(rows, table.Rows()) {
		t.Fatalf("whole activation lost row/budget provenance: rows=%+v budgets=%+v", rows, budgets)
	}
}

func TestCapacityTableArchiveActivatesOnlyWholeTerminalGroups(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial archive", true: "built and rejected archive"}[whole], func(t *testing.T) {
			settings, snapshot := capacityLifecycleFixture(2)
			load := func(_ context.Context, keys []string) (map[string]CapacityRow, error) {
				kappa := 123.0
				rows := map[string]CapacityRow{keys[0]: {Key: keys[0], Status: CapacityBuilt, Kappa: &kappa}}
				if whole {
					rows[keys[1]] = CapacityRow{Key: keys[1], Status: CapacityRejected, Reason: CapacityNonMonotone}
				}
				return rows, nil
			}
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, load))
			table.Ensure(context.Background(), settings, capacityLifecycleBudgets(snapshot, 10))
			first := nextCapacityBuild(t, started)
			waitCapacityLoad(t, table)
			if whole {
				rows, budgets := table.Inputs()
				if len(rows) != 2 || len(budgets) != 1 || rows[0].Status != CapacityBuilt || rows[1].Status != CapacityRejected ||
					!rows[0].Previous || !rows[1].Previous || rows[1].Reason != CapacityNonMonotone || rows[1].Kappa != nil {
					t.Fatalf("complete terminal archive was not activated intact: rows=%+v budgets=%+v", rows, budgets)
				}
				for _, row := range rows {
					if row.Key != row.Input.Key() || row.Input.CompletionMarginMS != budgets[0].TotalMS || row.Input.PostMarginMS != budgets[0].PostMS {
						t.Fatalf("archived row mismatches active input: %+v", row)
					}
				}
			} else {
				rows := assertNoActiveCapacityGroup(t, table, 10, 2)
				if !rows[0].Previous || rows[0].Status != CapacityBuilt || rows[1].Status != CapacityPending {
					t.Fatalf("partial archive diagnostics disappeared: %+v", rows)
				}
			}
			close(first.release)
			second := nextCapacityBuild(t, started)
			if !whole {
				assertNoActiveCapacityGroup(t, table, 10, 2)
			}
			close(second.release)
			table.Wait()
			assertCapacityGroup(t, table, 10, 2)
			for _, row := range table.Rows() {
				if row.Previous {
					t.Fatal("fresh whole build retained an archived stand-in")
				}
			}
		})
	}
}

func TestCapacityTableStructuralInvalidationWithdrawsAppliedInputs(t *testing.T) {
	for _, change := range []string{"definition", "profile", "occupancy", "capacity settings", "no variants"} {
		t.Run(change, func(t *testing.T) {
			settings, snapshot := capacityLifecycleFixture(1)
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
			snapshot = capacityLifecycleBudgets(snapshot, 10)
			table.Ensure(context.Background(), settings, snapshot)
			close(nextCapacityBuild(t, started).release)
			table.Wait()
			switch change {
			case "definition":
				w := *snapshot.Workloads[0]
				w.Generation++
				snapshot.Workloads = []*Definition{&w}
				snapshot = capacityLifecycleBudgets(snapshot, 10)
			case "profile":
				p := *snapshot.Options[0].Instance.Profile
				p.ID += "-replacement"
				snapshot.Options[0].Instance.Profile = &p
			case "occupancy":
				snapshot.DispatchOccupancy[0].PerRequest++
			case "capacity settings":
				settings.CapacityGridSteps++
			case "no variants":
				snapshot.Options = nil
			}
			table.Ensure(context.Background(), settings, snapshot)
			variants := 1
			if change == "no variants" {
				variants = 0
			}
			assertNoActiveCapacityGroup(t, table, 10, variants)
			if variants > 0 {
				close(nextCapacityBuild(t, started).release)
				table.Wait()
				assertCapacityGroup(t, table, 10, 1)
			} else {
				table.Wait()
				if rows, budgets := table.Inputs(); len(rows) != 0 || len(budgets) != 0 {
					t.Fatalf("empty group manufactured an applied budget: rows=%+v budgets=%+v", rows, budgets)
				}
			}
		})
	}
}
