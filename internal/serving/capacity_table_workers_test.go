package serving

import (
	"context"
	"reflect"
	"runtime"
	"testing"
)

func TestCapacityTableWorkerLimitShrinksByDrainingAndGrowsImmediately(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(6)
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(3, nil, nil))
	table.Ensure(context.Background(), settings, snapshot)
	admitted := []*capacityBuildBoundary{nextCapacityBuild(t, started), nextCapacityBuild(t, started), nextCapacityBuild(t, started)}
	table.SetWorkerLimit(1)
	if status := table.WorkerStatus(); status.Limit != 1 || status.Active != 3 {
		t.Fatalf("lower limit lost the actual draining count: %+v", status)
	}
	awaitActive := func(want int) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			table.mu.Lock()
			for table.workers != want {
				table.idle.Wait()
			}
			table.mu.Unlock()
			close(done)
		}()
		awaitCapacityBoundary(t, done)
	}
	for index, boundary := range admitted {
		if boundary.ctx.Err() != nil {
			t.Fatal("worker limit canceled valid numerical work")
		}
		close(boundary.release)
		if index < 2 {
			awaitActive(2 - index)
			select {
			case <-started:
				t.Fatal("shrink admitted a queued row before draining below limit")
			default:
			}
		}
	}
	fourth := nextCapacityBuild(t, started)
	if status := table.WorkerStatus(); status.Active != 1 {
		t.Fatalf("shrunken limit did not bound successor admission: %+v", status)
	}
	table.SetWorkerLimit(3)
	fifth, sixth := nextCapacityBuild(t, started), nextCapacityBuild(t, started)
	for _, boundary := range []*capacityBuildBoundary{fourth, fifth, sixth} {
		if boundary.ctx.Err() != nil {
			t.Fatal("grow canceled already admitted work")
		}
		close(boundary.release)
	}
	table.Wait()
	before := table.Rows()
	if len(before) != 6 {
		t.Fatalf("limit changes lost numerical rows: %+v", before)
	}
	for _, row := range before {
		if row.Status != CapacityBuilt {
			t.Fatalf("drained result was discarded: %+v", row)
		}
	}
	settings.CapacityWorkers = 2
	table.SetWorkerLimit(settings.CapacityWorkers)
	table.Ensure(context.Background(), settings, snapshot)
	if !reflect.DeepEqual(before, table.Rows()) || table.WorkerStatus().Active != 0 {
		t.Fatal("worker-only policy changed row keys, results, or started numerical work")
	}
}

func TestCapacityTableAutoWorkersBoundActualAdmission(t *testing.T) {
	want := max(1, runtime.GOMAXPROCS(0)/2)
	settings, snapshot := capacityLifecycleFixture(want + 1)
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(0, nil, nil))
	table.Ensure(context.Background(), settings, snapshot)
	admitted := make([]*capacityBuildBoundary, want)
	for index := range admitted {
		admitted[index] = nextCapacityBuild(t, started)
	}
	if status := table.WorkerStatus(); status.Limit != want || status.Active != want {
		t.Fatalf("auto admission must use half GOMAXPROCS, minimum one: %+v want=%d", status, want)
	}
	select {
	case <-started:
		t.Fatal("auto admitted beyond the resolved worker limit")
	default:
	}
	for _, boundary := range admitted {
		close(boundary.release)
	}
	close(nextCapacityBuild(t, started).release)
	table.Wait()
	if len(table.Rows()) != want+1 || table.WorkerStatus().Active != 0 {
		t.Fatal("auto queue failed to drain all rows")
	}
}

func TestCapacityTableWorkerStatusIncludesRecordingUntilIdle(t *testing.T) {
	settings, snapshot := capacityLifecycleFixture(1)
	recording, release := make(chan struct{}), make(chan struct{}, 1)
	defer close(release)
	table, started, _ := gateCapacityTableBuilds(t, NewCapacityTable(1, func(CapacityRow) error {
		close(recording)
		<-release
		return nil
	}, nil))
	table.Ensure(context.Background(), settings, snapshot)
	close(nextCapacityBuild(t, started).release)
	awaitCapacityBoundary(t, recording)
	if table.Rows()[0].Status != CapacityBuilt || table.WorkerStatus().Active != 1 {
		t.Fatal("terminal rows falsely reported idle before record completion")
	}
	release <- struct{}{}
	table.Wait()
	if table.WorkerStatus().Active != 0 {
		t.Fatal("finished table did not report idle")
	}
}
