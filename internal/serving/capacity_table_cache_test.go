package serving

import (
	"context"
	"testing"
)

func TestCapacityTableStructuralRefreshKeepsSharedNumericalWork(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(2)
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, nil, nil))
	snapshot = capacityLifecycleBudgets(snapshot, 10)
	table.Ensure(context.Background(), settings, snapshot)
	first := nextCapacityBuild(t, started)
	close(first.release)
	running := nextCapacityBuild(t, started)
	before := table.Rows()[0]
	changed := *snapshot.Workloads[0]
	changed.Generation++
	snapshot.Workloads = []*Definition{&changed}
	snapshot = capacityLifecycleBudgets(snapshot, 10)
	table.Ensure(context.Background(), settings, snapshot)
	if running.ctx.Err() != nil {
		t.Fatal("same numerical row was canceled by a definition provenance refresh")
	}
	rows := assertNoActiveCapacityGroup(t, table, 10, 2)
	if rows[0].Key != before.Key || rows[0].Status != CapacityBuilt || !rows[0].BuiltAt.Equal(before.BuiltAt) {
		t.Fatalf("structural refresh discarded a completed shared row: before=%+v after=%+v", before, rows[0])
	}
	close(running.release)
	table.Wait()
	assertCapacityGroup(t, table, 10, 2)
	_, budgets := table.Inputs()
	if budgets[0].DefinitionID != changed.Identity() {
		t.Fatalf("shared numerical work retained old definition provenance: %+v", budgets)
	}
	// Removing one variant must retain the completed row still needed by the group.
	snapshot.Options[0].Instance.MaxBatch = 1
	table.Ensure(context.Background(), settings, snapshot)
	table.Wait()
	assertCapacityGroup(t, table, 10, 1)
	if row := table.Rows()[0]; row.Key != before.Key || !row.BuiltAt.Equal(before.BuiltAt) {
		t.Fatalf("variant removal repeated retained numerical work: %+v", row)
	}
	select {
	case task := <-started:
		t.Fatalf("same numerical work was admitted twice: %+v", task)
	default:
	}
}
