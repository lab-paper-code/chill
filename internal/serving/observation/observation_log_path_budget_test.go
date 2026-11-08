package observation

import (
	"context"
	"fmt"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

const (
	pathBudgetFinishedPhase = "finished"
	pathBudgetLateRequest   = "late"
)

func appendPathBudgetHTTP(t *testing.T, log *ObservationLog, d *serving.Definition, id string, received time.Time, outcome string, direct bool) {
	t.Helper()
	assignment := &serving.Assignment{Instance: "path-instance", Node: serving.NodeReference{UID: "node"}, DispatchedAt: received.Add(30 * time.Millisecond)}
	for _, stage := range []struct {
		kind string
		ms   int
	}{
		{"received", 0}, {"accepted", 10}, {"assigned", 30},
	} {
		event := serving.RequestEvent{RequestID: id, WorkloadUID: d.UID, Definition: d.Identity(), Kind: stage.kind,
			At: received.Add(time.Duration(stage.ms) * time.Millisecond), ReceivedAt: received}
		if stage.kind == "assigned" {
			event.Assignment = assignment
		}
		if err := log.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if direct {
		appendPathBudgetNative(t, log, d, id, received, false, "path-instance")
	}
	if err := log.RequestEvent(serving.RequestEvent{RequestID: id, WorkloadUID: d.UID, Definition: d.Identity(),
		Kind: "response_finished", At: received.Add(100 * time.Millisecond), ReceivedAt: received,
		ElapsedNanos: int64(100 * time.Millisecond), Outcome: outcome, ExecutionComplete: outcome == outcomeCompleted}); err != nil {
		t.Fatal(err)
	}
	var timing serving.RequestTimings
	timing.Record(serving.ServerSenderFreeze, 35*time.Millisecond)
	if err := log.RequestClosed(id, timing); err != nil {
		t.Fatal(err)
	}
}

func appendPathBudgetNative(t *testing.T, log *ObservationLog, d *serving.Definition, id string, received time.Time, late bool, instance string) {
	t.Helper()
	worker := time.Unix(123, 0)
	for _, phase := range []struct {
		name string
		ms   int
	}{
		{"queued", 0}, {"running", 8}, {pathBudgetFinishedPhase, 40},
	} {
		progress := &serving.BackendProgress{Phase: phase.name, At: worker.Add(time.Duration(phase.ms) * time.Millisecond),
			BatchID: 7, BatchSize: 4, QueueMS: 10, NativeElapsedMS: 50}
		if phase.name == pathBudgetFinishedPhase {
			progress.ExecutionComplete, progress.Outcome = true, outcomeCompleted
		}
		event := serving.RequestEvent{RequestID: id, WorkloadUID: d.UID, Definition: d.Identity(), Kind: "backend_" + phase.name,
			At: received.Add(time.Duration(40+phase.ms) * time.Millisecond), NativeInstance: instance, NativeProgress: progress,
			ExecutionComplete: phase.name == pathBudgetFinishedPhase}
		var err error
		if late {
			err = log.RecordNativeSupplement(event)
		} else {
			err = log.RequestEvent(event)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func findPathBudget(t *testing.T, window LoadWindow, definition string) serving.PathBudget {
	t.Helper()
	for _, budget := range window.PathBudgets {
		if budget.DefinitionID == definition {
			return budget
		}
	}
	t.Fatalf("missing definition %s in %+v", definition, window.PathBudgets)
	return serving.PathBudget{}
}

func TestLoadWindowPathBudgetsReuseHTTPPopulationAndLateNative(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{Definitions: []string{"unrelated"}, Outcomes: []string{"canceled"}}, observationTestHour)
	from := observationTestHour.Add(time.Second)
	to := from.Add(time.Second)
	clock.set(to.Add(time.Second))
	d := &serving.Definition{UID: "path-workload", Generation: 1, SLO: serving.SLO{Percentile: 95}}
	changed := &serving.Definition{UID: d.UID, Generation: 2, SLO: d.SLO}
	for _, definition := range []*serving.Definition{d, changed} {
		if err := log.DefinitionRecord(definition); err != nil {
			t.Fatal(err)
		}
	}
	received := from.Add(100 * time.Millisecond)
	for i := range 118 {
		appendPathBudgetHTTP(t, log, d, fmt.Sprintf("complete-%d", i), received, outcomeCompleted, true)
	}
	appendPathBudgetHTTP(t, log, d, pathBudgetLateRequest, received, outcomeCompleted, false)
	appendPathBudgetHTTP(t, log, d, "failed", received, outcomeCanceled, false)
	appendPathBudgetHTTP(t, log, changed, "changed", received, outcomeCompleted, true)
	window, err := log.LoadWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	before := findPathBudget(t, window, d.Identity())
	if before.Source != serving.PathBudgetConfigured || before.Samples != 118 || before.Missing != 2 || before.Completed != 120 || before.Failed != 1 {
		t.Fatalf("deficient completed population: %+v", before)
	}
	// Direct physical evidence can complete path intervals, but cannot repair a
	// failed HTTP outcome or lend support across immutable definitions.
	appendPathBudgetNative(t, log, d, pathBudgetLateRequest, received, true, "wrong-instance")
	appendPathBudgetNative(t, log, d, "failed", received, true, "path-instance")
	window, err = log.LoadWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if budget := findPathBudget(t, window, d.Identity()); budget.Source != serving.PathBudgetConfigured || budget.Samples != 118 {
		t.Fatalf("foreign instance or late failed HTTP became sample: %+v", budget)
	}
	appendPathBudgetNative(t, log, d, pathBudgetLateRequest, received, true, "path-instance")
	window, err = log.LoadWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	assertPathBudgetLateNative(t, window, d, changed)
	assertPathBudgetHTTPQuery(t, log, d, from, to)
}

func assertPathBudgetLateNative(t *testing.T, window LoadWindow, d, changed *serving.Definition) {
	t.Helper()
	budget := findPathBudget(t, window, d.Identity())
	if budget.Source != serving.PathBudgetObserved || budget.Samples != 119 || budget.Completed != 120 || budget.Missing != 1 || budget.TotalMS != 32 || budget.PostMS != 15 || budget.Batches != 1 || budget.MaxBatchSamples != 119 {
		t.Fatalf("late direct evidence did not complete request-weighted paths: %+v", budget)
	}
	if other := findPathBudget(t, window, changed.Identity()); other.Source != serving.PathBudgetConfigured || other.Samples != 1 {
		t.Fatalf("changed definition borrowed old samples: %+v", other)
	}
	if len(window.Arrivals) != 121 || len(window.Workloads) != 1 || window.Workloads[0].Received != 121 || window.Workloads[0].Completed != 120 {
		t.Fatalf("native supplements changed scheduler ingress/completion counts: %+v", window)
	}
}

func assertPathBudgetHTTPQuery(t *testing.T, log *ObservationLog, d *serving.Definition, from, to time.Time) {
	t.Helper()
	ctx := context.Background()
	query, err := log.Query(ctx, d.UID, from, to, []string{d.Identity()}, []string{outcomeCompleted}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	foundLate := false
	for _, row := range query.Requests {
		if row.RequestID == pathBudgetLateRequest {
			foundLate = true
		}
		if row.RequestID == pathBudgetLateRequest && (row.NativeQueuedAt != nil || row.NativeRunningAt != nil || row.NativeFinishedAt != nil) {
			t.Fatalf("path-only supplement changed ordinary HTTP query: %+v", row)
		}
	}
	if !foundLate {
		t.Fatal("ordinary HTTP query lost completed request")
	}
}

func TestLoadWindowPathBudgetsKeepReceiptAndCompletionBoundaries(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	from := observationTestHour.Add(time.Second)
	to := from.Add(time.Second)
	clock.set(to.Add(time.Second))
	d := &serving.Definition{UID: "path-workload", SLO: serving.SLO{Percentile: 95}}
	if err := log.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	appendPathBudgetHTTP(t, log, d, "old", from.Add(-100*time.Millisecond), outcomeCompleted, true)
	appendPathBudgetHTTP(t, log, d, "at-end", to.Add(-100*time.Millisecond), outcomeCompleted, true)
	appendPathBudgetHTTP(t, log, d, "incomplete", from.Add(100*time.Millisecond), outcomeIncomplete, false)
	appendPathBudgetNative(t, log, d, "incomplete", from.Add(100*time.Millisecond), true, "path-instance")
	if err := log.RequestEvent(serving.RequestEvent{RequestID: "open", WorkloadUID: d.UID, Definition: d.Identity(), Kind: "received", At: from}); err != nil {
		t.Fatal(err)
	}
	window, err := log.LoadWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	budget := findPathBudget(t, window, d.Identity())
	if budget.Received != 3 || budget.Completed != 2 || budget.Samples != 1 || budget.Missing != 1 || budget.Incomplete != 3 || budget.Failed != 0 {
		t.Fatalf("late native or receipt/response boundary changed population: %+v", budget)
	}
}
