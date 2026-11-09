package observation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestCapacityArchiveRecoveryReusesNumericalRows(t *testing.T) {
	ctx := context.Background()
	settings, snapshot := capacityFixture()
	settings.CapacityWorkers = 2
	archive := newReadableObservationArchive(t)
	log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
	record := func(row serving.CapacityRow) error {
		return log.ManagementEvent(serving.ManagementEvent{WorkloadUID: row.Input.WorkloadUID, Kind: serving.CapacityRowBuiltEvent, At: row.BuiltAt, Capacity: &row})
	}
	original := serving.NewCapacityTable(settings.CapacityWorkers, record, nil)
	original.Ensure(ctx, settings, snapshot)
	original.Wait()
	before := original.Rows()[0]
	if before.Status != serving.CapacityBuilt || before.Kappa == nil || before.BuiltAt.IsZero() {
		t.Fatalf("real builder did not produce a reusable result: %+v", before)
	}
	clock.set(observationTestHour.Add(3 * time.Hour))
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour.Add(3*time.Hour))
	var builds atomic.Int32
	table := serving.NewCapacityTable(settings.CapacityWorkers, func(serving.CapacityRow) error { builds.Add(1); return nil }, restarted.CapacityRecords)
	table.Ensure(ctx, settings, snapshot)
	table.Wait()
	rows, budgets := table.Inputs()
	if builds.Load() != 0 || len(rows) != 1 || len(budgets) != 1 || rows[0].Key != before.Key || !rows[0].BuiltAt.Equal(before.BuiltAt) || rows[0].Kappa == nil || *rows[0].Kappa != *before.Kappa {
		t.Fatalf("archive recovery repeated numerical work: builds=%d rows=%+v budgets=%+v original=%+v", builds.Load(), rows, budgets, before)
	}
	if status := table.WorkerStatus(); status.Limit != 2 || status.Active != 0 {
		t.Fatalf("recovery lost bounded idle worker status: %+v", status)
	}
	snapshot.Workloads[0].Generation++
	snapshot.DispatchOccupancy[0].Source = "refreshed-evidence"
	table.Ensure(ctx, settings, snapshot)
	table.Wait()
	if row := table.Rows()[0]; builds.Load() != 0 || row.Key != before.Key || !row.BuiltAt.Equal(before.BuiltAt) || row.Input.DispatchOccupancy.Source != "refreshed-evidence" {
		t.Fatalf("provenance refresh repeated numerical work or lost provenance: builds=%d row=%+v", builds.Load(), row)
	}
	snapshot.DispatchOccupancy[0].PerRequest = time.Millisecond
	table.Ensure(ctx, settings, snapshot)
	table.Wait()
	if row := table.Rows()[0]; builds.Load() != 1 || row.Key == before.Key || row.Input.DispatchOccupancy.PerRequest != time.Millisecond {
		t.Fatalf("changed scientific input reused stale capacity: builds=%d row=%+v", builds.Load(), row)
	}
}

func TestCapacityArchiveUnsuitableRowsRebuild(t *testing.T) {
	ctx := context.Background()
	settings, snapshot := capacityFixture()
	budget := serving.ResolvePathBudget(settings, nil, snapshot.Workloads[0])
	built, err := serving.BuildCapacityRow(ctx, settings, snapshot.DispatchOccupancy, snapshot.Options[0], snapshot.Workloads[0], snapshot.At, budget)
	if err != nil || built.Status != serving.CapacityBuilt {
		t.Fatalf("real builder did not complete: %+v %v", built, err)
	}
	for _, kind := range []string{"old calculation", "missing", "pending", "missing kappa", "negative kappa", "unfinished", "runtime error", "archive error"} {
		t.Run(kind, func(t *testing.T) {
			archive := newReadableObservationArchive(t)
			log, clock := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour)
			row := built
			switch kind {
			case "old calculation":
				row.Key = "shared-cpu/" + row.Key[strings.IndexByte(row.Key, '/')+1:]
			case "pending":
				row.Status = serving.CapacityPending
			case "missing kappa":
				row.Kappa = nil
			case "negative kappa":
				negative := -1.0
				row.Kappa = &negative
			case "unfinished":
				row.BuiltAt = time.Time{}
			case "runtime error":
				row.Status, row.Reason, row.Kappa = serving.CapacityRejected, context.DeadlineExceeded.Error(), nil
			}
			if kind != "missing" {
				if err := log.ManagementEvent(serving.ManagementEvent{WorkloadUID: row.Input.WorkloadUID, Kind: serving.CapacityRowBuiltEvent, At: observationTestHour, Capacity: &row}); err != nil {
					t.Fatal(err)
				}
			}
			clock.set(observationTestHour.Add(3 * time.Hour))
			if err := log.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			restarted, _ := clockedObservationLog(t, ObservationLogConfig{Writer: archive}, observationTestHour.Add(3*time.Hour))
			if kind == "archive error" {
				archive.openError = errors.New("archive unavailable")
			}
			var builds atomic.Int32
			table := serving.NewCapacityTable(2, func(serving.CapacityRow) error { builds.Add(1); return nil }, restarted.CapacityRecords)
			table.Ensure(ctx, settings, snapshot)
			table.Wait()
			rows, budgets := table.Inputs()
			if builds.Load() != 1 || len(rows) != 1 || len(budgets) != 1 || rows[0].Status != serving.CapacityBuilt || rows[0].Key != built.Key || rows[0].BuiltAt.Equal(built.BuiltAt) {
				t.Fatalf("unsuitable archive became completed cache: builds=%d rows=%+v budgets=%+v", builds.Load(), rows, budgets)
			}
		})
	}
}
