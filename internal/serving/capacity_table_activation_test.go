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
			snapshot = capacityLifecycleBudgets(snapshot, 10)
			archived := map[string]CapacityRow{}
			for _, option := range CapacityVariants(snapshot.Options[0]) {
				row, err := BuildCapacityRow(context.Background(), settings, snapshot.DispatchOccupancy, option, snapshot.Workloads[0], snapshot.At, snapshot.PathBudgets[0])
				if err != nil {
					t.Fatal(err)
				}
				if option.Instance.MaxBatch == 2 {
					if !whole {
						continue
					}
					row.Status, row.Reason, row.Kappa = CapacityRejected, CapacityNonMonotone, nil
				}
				archived[row.Key] = row
			}
			load := func(context.Context, []string) (map[string]CapacityRow, error) { return archived, nil }
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, load))
			table.Ensure(context.Background(), settings, snapshot)
			waitCapacityLoad(t, table)
			if whole {
				table.Wait()
				rows, budgets := table.Inputs()
				if len(rows) != 2 || len(budgets) != 1 || rows[0].Status != CapacityBuilt || rows[1].Status != CapacityRejected ||
					rows[1].Reason != CapacityNonMonotone || rows[1].Kappa != nil || table.WorkerStatus().Active != 0 {
					t.Fatalf("complete scientific archive was not activated intact: rows=%+v budgets=%+v", rows, budgets)
				}
			} else {
				running := nextCapacityBuild(t, started)
				rows := assertNoActiveCapacityGroup(t, table, 10, 2)
				if rows[0].Status != CapacityBuilt || rows[1].Status != CapacityPending || running.batch != 2 {
					t.Fatalf("partial archive admitted duplicate work: rows=%+v task=%+v", rows, running)
				}
				close(running.release)
				table.Wait()
				assertCapacityGroup(t, table, 10, 2)
			}
			select {
			case running := <-started:
				t.Fatalf("completed archived row was rebuilt: %+v", running)
			default:
			}
		})
	}
}

func TestCapacityTableStructuralInvalidationWithdrawsAppliedInputs(t *testing.T) {
	for _, change := range []string{"profile", "occupancy", "capacity settings", "no variants"} {
		t.Run(change, func(t *testing.T) {
			settings, snapshot := capacityLifecycleFixture(1)
			table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
			snapshot = capacityLifecycleBudgets(snapshot, 10)
			table.Ensure(context.Background(), settings, snapshot)
			close(nextCapacityBuild(t, started).release)
			table.Wait()
			switch change {
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
